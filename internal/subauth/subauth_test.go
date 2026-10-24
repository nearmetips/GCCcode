package subauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/nearmetips/DailiCode/internal/codex"
	"github.com/nearmetips/DailiCode/internal/core"
)

func jwt(exp int64) string {
	enc := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	return enc(map[string]string{"alg": "none"}) + "." + enc(map[string]any{"exp": exp}) + ".sig"
}

func TestCodexFreshTokenNeedsNoNetwork(t *testing.T) {
	t.Setenv("GCCCODE_HOME", t.TempDir())
	access := jwt(time.Now().Add(time.Hour).Unix())
	raw := fmt.Sprintf(`{"tokens":{"access_token":"%s","refresh_token":"r","account_id":"acct-a"}}`, access)
	if err := codex.SaveVault("acc-a", []byte(raw)); err != nil {
		t.Fatal(err)
	}
	st, _ := core.Load()
	acc := core.Account{ID: "acc-a", Kind: core.KindCodex, RemoteID: "u:acct-a"}
	s := New(func(core.Account) *http.Client {
		t.Fatal("should not refresh a fresh token")
		return nil
	})
	got, err := s.Codex(context.Background(), st.Settings, acc, false)
	if err != nil {
		t.Fatal(err)
	}
	if got.Access != access || got.AccountID != "acct-a" {
		t.Fatalf("unexpected token: %+v", got)
	}
}

func TestCodexExpiredTokenRefreshesAndSavesVault(t *testing.T) {
	t.Setenv("GCCCODE_HOME", t.TempDir())
	old := jwt(time.Now().Add(-time.Minute).Unix())
	raw := fmt.Sprintf(`{"tokens":{"access_token":"%s","refresh_token":"r-old","account_id":"acct-a"}}`, old)
	if err := codex.SaveVault("acc-a", []byte(raw)); err != nil {
		t.Fatal(err)
	}
	fresh := jwt(time.Now().Add(time.Hour).Unix())
	var gotRefresh string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var in map[string]string
		_ = json.Unmarshal(b, &in)
		gotRefresh = in["refresh_token"]
		_, _ = io.WriteString(w, fmt.Sprintf(`{"id_token":"%s","access_token":"%s","refresh_token":"r-new"}`, fresh, fresh))
	}))
	defer up.Close()
	CodexTokenURL = up.URL

	st, _ := core.Load()
	acc := core.Account{ID: "acc-a", Kind: core.KindCodex, RemoteID: "u:acct-a"}
	s := New(func(core.Account) *http.Client { return up.Client() })
	got, err := s.Codex(context.Background(), st.Settings, acc, false)
	if err != nil {
		t.Fatal(err)
	}
	if gotRefresh != "r-old" {
		t.Fatalf("refresh request used %q, want r-old", gotRefresh)
	}
	if got.Access != fresh {
		t.Fatalf("did not return the refreshed token")
	}
	// The vault now holds the refreshed token.
	vb, _ := os.ReadFile(codex.VaultPath("acc-a"))
	nt, err := codex.ReadTokens(vb)
	if err != nil {
		t.Fatal(err)
	}
	if nt.Access != fresh || nt.Refresh != "r-new" {
		t.Fatalf("vault not updated: %+v", nt)
	}
}

func TestClaudeExpiredTokenRefreshesFile(t *testing.T) {
	t.Setenv("GCCCODE_HOME", t.TempDir())
	dir := t.TempDir()
	cfg := `{"userID":"dev-a","oauthAccount":{"accountUuid":"uuid-a","emailAddress":"a@x.com"}}`
	_ = os.WriteFile(dir+"/.claude.json", []byte(cfg), 0o600)
	creds := fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"old","refreshToken":"r-old","expiresAt":%d,"scopes":["user:inference"]}}`,
		time.Now().Add(-time.Minute).UnixMilli())
	_ = os.WriteFile(dir+"/.credentials.json", []byte(creds), 0o600)

	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"access_token":"new","refresh_token":"r-new","expires_in":3600,"scope":"user:inference"}`)
	}))
	defer up.Close()
	ClaudeTokenURL = up.URL

	st, _ := core.Load()
	acc := core.Account{ID: "cl-a", Kind: core.KindClaude, ConfigDir: dir}
	s := New(func(core.Account) *http.Client { return up.Client() })
	got, err := s.Claude(context.Background(), st.Settings, acc, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if got.Access != "new" {
		t.Fatalf("want refreshed token, got %q", got.Access)
	}
	b, _ := os.ReadFile(dir + "/.credentials.json")
	if !jsonHas(b, "new") {
		t.Fatalf("credentials file not updated: %s", b)
	}
}

func jsonHas(b []byte, token string) bool {
	var doc struct {
		OAuth struct {
			AccessToken string `json:"accessToken"`
		} `json:"claudeAiOauth"`
	}
	_ = json.Unmarshal(b, &doc)
	return doc.OAuth.AccessToken == token
}
