package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nearmetips/DailiCode/internal/core"
)

func TestRejectsPublicBind(t *testing.T) {
	if _, err := listenLoopback("0.0.0.0:4725"); err == nil {
		t.Fatal("accepted a non-loopback address")
	}
}

func TestGuardKeepsOtherSitesOut(t *testing.T) {
	h := guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	cases := []struct {
		name, method, host, origin string
		header                     bool
		want                       int
	}{
		{"panel read", http.MethodGet, "127.0.0.1:4725", "", false, http.StatusNoContent},
		{"localhost read", http.MethodGet, "localhost:4725", "", false, http.StatusNoContent},
		{"dns rebinding", http.MethodGet, "evil.example:4725", "", false, http.StatusForbidden},
		{"write without header", http.MethodPost, "127.0.0.1:4725", "", false, http.StatusForbidden},
		{"write from another site", http.MethodPost, "127.0.0.1:4725", "http://evil.example", true, http.StatusForbidden},
		{"write from the panel", http.MethodPost, "127.0.0.1:4725", "http://127.0.0.1:4725", true, http.StatusNoContent},
	}
	for _, c := range cases {
		req := httptest.NewRequest(c.method, "http://"+c.host+"/api/x", nil)
		if c.header {
			req.Header.Set(actionHeader, "1")
		}
		if c.origin != "" {
			req.Header.Set("Origin", c.origin)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("%s: got %d, want %d", c.name, rec.Code, c.want)
		}
	}
}

type calls struct{ ran, opened [][]string }

func stubActions(t *testing.T) *calls {
	t.Helper()
	t.Setenv("GCCCODE_HOME", t.TempDir())
	c := &calls{}
	oldRun, oldOpen, oldBusy := runSelf, openTerminal, codexRunning
	runSelf = func(args ...string) (string, error) { c.ran = append(c.ran, args); return "done", nil }
	openTerminal = func(args ...string) error { c.opened = append(c.opened, args); return nil }
	codexRunning = func() bool { return false }
	t.Cleanup(func() { runSelf, openTerminal, codexRunning = oldRun, oldOpen, oldBusy })
	if _, err := core.Mutate(func(s *core.Store) error {
		s.Accounts = append(s.Accounts, core.Account{ID: "cl-1", Kind: core.KindClaude, Label: "cl-1"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return c
}

func post(h http.HandlerFunc, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)))
	return rec
}

func TestLoginOpensTerminal(t *testing.T) {
	c := stubActions(t)
	if rec := post(handleLogin, `{"kind":"claude"}`); rec.Code != http.StatusOK {
		t.Fatalf("add: %d %s", rec.Code, rec.Body)
	}
	if rec := post(handleLogin, `{"id":"cl-1"}`); rec.Code != http.StatusOK {
		t.Fatalf("login: %d %s", rec.Code, rec.Body)
	}
	if rec := post(handleLogin, `{"kind":"evil; rm -rf ~"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown kind: %d", rec.Code)
	}
	want := [][]string{{"account", "add", "claude"}, {"account", "login", "cl-1"}}
	if !reflect.DeepEqual(c.opened, want) {
		t.Fatalf("opened %v, want %v", c.opened, want)
	}
}

func TestCodexLoginWaitsForCodexToQuit(t *testing.T) {
	c := stubActions(t)
	codexRunning = func() bool { return true }
	rec := post(handleLogin, `{"kind":"codex"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "先完全退出") {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	if len(c.opened) != 0 {
		t.Fatalf("opened a terminal while Codex runs: %v", c.opened)
	}
}

func TestAccountButtonsRunTheSameCommands(t *testing.T) {
	c := stubActions(t)
	for _, step := range []struct {
		h    http.HandlerFunc
		body string
	}{
		{handleProxy, `{"id":"cl-1","proxy":" http://127.0.0.1:7891 "}`},
		{handleProxy, `{"id":"cl-1","proxy":""}`},
		{handleEnable, `{"id":"cl-1","enabled":false}`},
		{handleImport, `{"kind":"codex"}`},
		{handleRemove, `{"id":"cl-1"}`},
	} {
		if rec := post(step.h, step.body); rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", step.body, rec.Code, rec.Body)
		}
	}
	if rec := post(handleRemove, `{"id":"nobody"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown account: %d", rec.Code)
	}
	want := [][]string{
		{"account", "proxy", "cl-1", "http://127.0.0.1:7891"},
		{"account", "proxy", "cl-1", "off"},
		{"account", "disable", "cl-1"},
		{"account", "import", "codex"},
		{"account", "rm", "cl-1"},
	}
	if !reflect.DeepEqual(c.ran, want) {
		t.Fatalf("ran %v, want %v", c.ran, want)
	}
}

func TestPastedTokenSwitchesTheAccountToIt(t *testing.T) {
	stubActions(t)
	if rec := post(handleToken, `{"id":"cl-1","token":"sk-ant-api03-`+strings.Repeat("x", 60)+`"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("accepted an API key: %d", rec.Code)
	}
	tok := "sk-ant-oat01-" + strings.Repeat("c", 60)
	if rec := post(handleToken, `{"id":"cl-1","token":"export CLAUDE_CODE_OAUTH_TOKEN=`+tok+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	st, _ := core.Load()
	a, _ := st.Account("cl-1")
	if a.TokenAt.IsZero() {
		t.Fatal("account not switched to its token")
	}
	b, err := os.ReadFile(core.ClaudeTokenFile("cl-1"))
	if err != nil || strings.TrimSpace(string(b)) != tok {
		t.Fatalf("saved %q, %v", b, err)
	}
	if fi, _ := os.Stat(core.ClaudeTokenFile("cl-1")); fi.Mode().Perm() != 0o600 {
		t.Fatalf("token file mode %v", fi.Mode().Perm())
	}
}

func TestScriptQuotesPaths(t *testing.T) {
	got := script("/Apps/G C/gcccode", "/tmp/it's", []string{"account", "add", "claude"})
	for _, want := range []string{
		`export GCCCODE_HOME='/tmp/it'\''s'`,
		`'/Apps/G C/gcccode' 'account' 'add' 'claude'`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("script missing %q:\n%s", want, got)
		}
	}
}

func TestOffersLoginNotInPool(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GCCCODE_HOME", t.TempDir())
	cfg := `{"oauthAccount":{"emailAddress":"me@example.com","accountUuid":"uuid-1"}}`
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := core.Load()
	if err != nil {
		t.Fatal(err)
	}
	if email, ok := unpooled(st, core.KindClaude); !ok || email != "me@example.com" {
		t.Fatalf("got %q %v, want the signed-in account offered", email, ok)
	}
	st.Accounts = append(st.Accounts, core.Account{ID: "me", Kind: core.KindClaude, RemoteID: "uuid-1"})
	if _, ok := unpooled(st, core.KindClaude); ok {
		t.Fatal("offered an account already in the pool")
	}
}
