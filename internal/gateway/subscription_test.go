package gateway

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nearmetips/DailiCode/internal/codex"
	"github.com/nearmetips/DailiCode/internal/core"
)

// jwt builds an unsigned JWT whose payload carries exp, enough for the token
// code that only reads claims.
func jwt(exp int64, extra map[string]any) string {
	claims := map[string]any{"exp": exp}
	for k, v := range extra {
		claims[k] = v
	}
	enc := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	return enc(map[string]string{"alg": "none"}) + "." + enc(claims) + ".sig"
}

func TestCodexDropReasoning(t *testing.T) {
	body := []byte(`{"model":"gpt","input":[{"type":"message","role":"user"},{"type":"reasoning","id":"r"},{"type":"message","role":"assistant"}]}`)
	out := codexDropReasoning(body)
	if bytes.Contains(out, []byte(`"reasoning"`)) {
		t.Fatalf("reasoning item was not dropped: %s", out)
	}
	var m map[string]json.RawMessage
	var in []json.RawMessage
	_ = json.Unmarshal(out, &m)
	_ = json.Unmarshal(m["input"], &in)
	if len(in) != 2 {
		t.Fatalf("want 2 input items, got %d", len(in))
	}
}

func TestRewriteClaudeMetadata(t *testing.T) {
	body := []byte(`{"model":"claude","metadata":{"user_id":"{\"device_id\":\"old\",\"account_uuid\":\"\",\"session_id\":\"s1\"}"}}`)
	out := rewriteClaudeMetadata(body, "dev-new", "uuid-new")
	var m struct {
		Metadata struct {
			UserID string `json:"user_id"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	var inner map[string]any
	if err := json.Unmarshal([]byte(m.Metadata.UserID), &inner); err != nil {
		t.Fatal(err)
	}
	if inner["device_id"] != "dev-new" || inner["account_uuid"] != "uuid-new" {
		t.Fatalf("metadata not rewritten: %v", inner)
	}
	if inner["session_id"] != "s1" {
		t.Fatalf("session id lost: %v", inner)
	}
}

func TestCodexQuotaHeaders(t *testing.T) {
	h := http.Header{}
	h.Set("x-codex-primary-used-percent", "42")
	h.Set("x-codex-primary-reset-at", "1800000000")
	q, ok := codexQuota(h, time.Now())
	if !ok || q.FiveHour == nil || q.FiveHour.UsedPct != 42 || q.FiveHour.ResetAt != 1800000000 {
		t.Fatalf("bad quota parse: %+v", q)
	}
}

func TestClaudeQuotaHeaders(t *testing.T) {
	h := http.Header{}
	h.Set("anthropic-ratelimit-unified-5h-utilization", "0.5")
	h.Set("anthropic-ratelimit-unified-5h-reset", "1800000000")
	q, ok := claudeQuota(h, time.Now())
	if !ok || q.FiveHour == nil || q.FiveHour.UsedPct != 50 {
		t.Fatalf("bad quota parse: %+v", q)
	}
}

// writeCodexAccount adds a Codex pool account with a vault login whose access
// token stays valid, so routing never needs the network to refresh.
func writeCodexAccount(t *testing.T, id, acct string) {
	t.Helper()
	access := jwt(time.Now().Add(time.Hour).Unix(), nil)
	raw := fmt.Sprintf(`{"tokens":{"id_token":"%s","access_token":"%s","refresh_token":"r","account_id":"%s"},"last_refresh":"%s"}`,
		access, access, acct, time.Now().UTC().Format(time.RFC3339Nano))
	if err := codex.SaveVault(id, []byte(raw)); err != nil {
		t.Fatal(err)
	}
	if _, err := core.Mutate(func(s *core.Store) error {
		s.Accounts = append(s.Accounts, core.Account{ID: id, Kind: core.KindCodex, Label: id, RemoteID: "u:" + acct, CreatedAt: time.Now()})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func newTestServer(cfg core.Gateway) *Server {
	s := New(func() (core.Gateway, error) { return cfg, nil })
	return s
}

func post(t *testing.T, s *Server, path, token string, header map[string]string, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:3426"+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func TestCodexSubFailoverAndQuota(t *testing.T) {
	t.Setenv("GCCCODE_HOME", t.TempDir())
	writeCodexAccount(t, "acc-a", "acct-a")
	writeCodexAccount(t, "acc-b", "acct-b")

	var hitA, hitB int
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		acct := r.Header.Get("chatgpt-account-id")
		if r.Header.Get("Authorization") == "" {
			t.Error("upstream got no Authorization")
		}
		switch acct {
		case "acct-a":
			hitA++
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"type":"usage_limit_reached","resets_at":1900000000}}`)
		case "acct-b":
			hitB++
			w.Header().Set("x-codex-primary-used-percent", "10")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"ok":true}`)
		default:
			t.Errorf("unexpected account id %q", acct)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer up.Close()
	t.Setenv("GCCCODE_GATEWAY_TEST_CODEX_URL", up.URL)

	cfg := core.Gateway{Token: "t", Providers: []core.GatewayProvider{{Name: "pool", Subscription: "codex"}}}
	s := newTestServer(cfg)
	rec := post(t, s, "/v1/responses", "t", nil, `{"model":"gpt-5","prompt_cache_key":"sess-1","input":[]}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Fatalf("did not fail over to acc-b: %s", rec.Body.String())
	}
	if hitA != 1 || hitB != 1 {
		t.Fatalf("hits a=%d b=%d", hitA, hitB)
	}
	st, _ := core.Load()
	if until, ok := st.State.Cooldown["acc-a"]; !ok || until <= time.Now().Unix() {
		t.Fatalf("acc-a not parked out of quota: %v", st.State.Cooldown)
	}
	if st.State.SessionOwner["sess-1"] != "acc-b" {
		t.Fatalf("session not bound to acc-b: %v", st.State.SessionOwner)
	}
}

// writeClaudeAccount adds a Claude pool account with a file-stored login.
func writeClaudeAccount(t *testing.T, id, uuid, device string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfgJSON := fmt.Sprintf(`{"userID":"%s","oauthAccount":{"emailAddress":"%s@x.com","accountUuid":"%s"}}`, device, id, uuid)
	if err := os.WriteFile(filepath.Join(dir, ".claude.json"), []byte(cfgJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	creds := fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"acc-token-%s","refreshToken":"r","expiresAt":%d,"scopes":["user:inference"]}}`,
		id, time.Now().Add(time.Hour).UnixMilli())
	if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte(creds), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := core.Mutate(func(s *core.Store) error {
		s.Accounts = append(s.Accounts, core.Account{ID: id, Kind: core.KindClaude, Label: id, ConfigDir: dir, CreatedAt: time.Now()})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestClaudeSubRoutesWithOwnCredentials(t *testing.T) {
	t.Setenv("GCCCODE_HOME", t.TempDir())
	writeClaudeAccount(t, "cl-a", "uuid-a", "dev-a")

	var gotAuth, gotDevice, gotBeta string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotBeta = strings.Join(r.Header.Values("Anthropic-Beta"), ",")
		b, _ := io.ReadAll(r.Body)
		var m struct {
			Metadata struct {
				UserID string `json:"user_id"`
			} `json:"metadata"`
		}
		_ = json.Unmarshal(b, &m)
		var inner map[string]any
		_ = json.Unmarshal([]byte(m.Metadata.UserID), &inner)
		if inner != nil {
			gotDevice, _ = inner["device_id"].(string)
		}
		if r.Header.Get("X-Api-Key") != "" {
			t.Error("x-api-key leaked upstream")
		}
		w.Header().Set("anthropic-ratelimit-unified-5h-utilization", "0.2")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"type":"message","content":[]}`)
	}))
	defer up.Close()
	t.Setenv("GCCCODE_GATEWAY_TEST_CLAUDE_URL", up.URL)

	cfg := core.Gateway{Token: "t", Providers: []core.GatewayProvider{{Name: "pool", Subscription: "claude"}}}
	s := newTestServer(cfg)
	body := `{"model":"claude-3-5","metadata":{"user_id":"{\"device_id\":\"orig\",\"session_id\":\"cs-1\"}"},"messages":[]}`
	rec := post(t, s, "/v1/messages", "t", map[string]string{
		"User-Agent":               "claude-cli/2.1.0",
		"x-app":                    "cli",
		"X-Claude-Code-Session-Id": "cs-1",
		"x-api-key":                "sk-should-be-dropped",
	}, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if gotAuth != "Bearer acc-token-cl-a" {
		t.Fatalf("upstream auth = %q", gotAuth)
	}
	if gotDevice != "dev-a" {
		t.Fatalf("device not rewritten: %q", gotDevice)
	}
	if !strings.Contains(gotBeta, claudeOAuthBeta) {
		t.Fatalf("oauth beta missing: %q", gotBeta)
	}
	st, _ := core.Load()
	if st.State.SessionOwner["cs-1"] != "cl-a" {
		t.Fatalf("session not bound: %v", st.State.SessionOwner)
	}
}

func TestChatViaCodexTranslation(t *testing.T) {
	t.Setenv("GCCCODE_HOME", t.TempDir())
	writeCodexAccount(t, "acc-a", "acct-a")

	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var rr respReq
		if err := json.Unmarshal(b, &rr); err != nil {
			t.Errorf("upstream got bad Responses body: %v", err)
		}
		if rr.Store {
			t.Error("store should be false")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi there"}]}],"usage":{"input_tokens":3,"output_tokens":2}}`)
	}))
	defer up.Close()
	t.Setenv("GCCCODE_GATEWAY_TEST_CODEX_URL", up.URL)

	cfg := core.Gateway{Token: "t", Providers: []core.GatewayProvider{{Name: "pool", Subscription: "codex"}}}
	s := newTestServer(cfg)
	rec := post(t, s, "/v1/chat/completions", "t", nil, `{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("bad chat json: %v %s", err, rec.Body.String())
	}
	if len(out.Choices) == 0 || out.Choices[0].Message.Content != "hi there" {
		t.Fatalf("translation lost text: %s", rec.Body.String())
	}
}
