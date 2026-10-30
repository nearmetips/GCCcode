package launcher

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nearmetips/DailiCode/internal/claude"
	"github.com/nearmetips/DailiCode/internal/core"
)

func TestManagementCommandsSkipThePool(t *testing.T) {
	cases := []struct {
		kind string
		args []string
		want bool
	}{
		{core.KindClaude, []string{"mcp", "add", "x"}, true},
		{core.KindClaude, []string{"--version"}, true},
		{core.KindClaude, []string{"update"}, true},
		{core.KindClaude, []string{"-p", "hi"}, false},
		{core.KindClaude, nil, false},
		{core.KindCodex, []string{"login"}, true},
		{core.KindCodex, []string{"mcp", "list"}, true},
		{core.KindCodex, []string{"exec", "fix it"}, false},
		{core.KindCodex, []string{"resume", "abc"}, false},
		{core.KindGemini, []string{"auth", "login"}, true},
		{core.KindGemini, []string{"--version"}, true},
		{core.KindGemini, []string{"-p", "hi"}, false},
		{core.KindGrok, []string{"login"}, true},
		{core.KindGrok, []string{"doctor"}, true},
		{core.KindGrok, []string{"-p", "hi"}, false},
		{core.KindAgy, []string{"models"}, true},
		{core.KindAgy, []string{"mcp", "list"}, true},
		{core.KindAgy, []string{"-p", "hi"}, false},
		{core.KindAgy, []string{"--conversation", "abc"}, false},
	}
	for _, c := range cases {
		if got := Management(c.kind, c.args); got != c.want {
			t.Errorf("Management(%s, %v) = %v", c.kind, c.args, got)
		}
	}
}

func writeTranscript(t *testing.T, home, project, id, cwd string, mod time.Time) string {
	t.Helper()
	p := filepath.Join(home, "projects", project, id+".jsonl")
	os.MkdirAll(filepath.Dir(p), 0o755)
	body := `{"type":"user","cwd":"` + cwd + `","message":{"role":"user","content":"hi"}}` + "\n"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	os.Chtimes(p, mod, mod)
	return p
}

func TestClaudeSessionFollowsTheLaunch(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	s := core.DefaultSettings()
	s.ClaudeHome = home
	now := time.Now()
	mine := writeTranscript(t, home, "work", "mine", work, now.Add(-time.Minute))
	writeTranscript(t, home, "work", "parallel", work, now)
	st := &core.Store{Settings: s, State: core.State{
		SessionOwner: map[string]string{"parallel": "claude-2"},
		Launches: map[string]core.Launch{
			"L1":    {SessionID: "mine", Transcript: mine, At: now},
			"stale": {SessionID: "x", At: now.Add(-8 * 24 * time.Hour)},
		},
	}}

	got, ok := claudeSession(st, "claude-1", "L1", work, now.Add(-time.Hour))
	if !ok || got.ID != "mine" {
		t.Fatalf("got %+v, want the session the status line recorded", got)
	}
	if _, left := st.State.Launches["L1"]; left {
		t.Fatal("launch record not consumed")
	}
	if _, left := st.State.Launches["stale"]; left {
		t.Fatal("week-old launch record not pruned")
	}

	got, ok = claudeSession(st, "claude-1", "L2", work, now.Add(-time.Hour))
	if !ok || got.ID != "mine" {
		t.Fatalf("got %+v, want the newest session no other account owns", got)
	}
}

func TestChildEnvIsolatesGeminiAndGrokHomes(t *testing.T) {
	s := core.DefaultSettings()
	s.GeminiHome = t.TempDir()
	s.GrokHome = t.TempDir()
	gem := core.Account{ID: "gemini-1", Kind: core.KindGemini, ConfigDir: filepath.Join(t.TempDir(), "g"), Proxy: "http://127.0.0.1:7890"}
	grk := core.Account{ID: "grok-1", Kind: core.KindGrok, ConfigDir: filepath.Join(t.TempDir(), "k")}
	t.Setenv("GEMINI_CONFIG_DIR", "/wrong")
	t.Setenv("GROK_HOME", "/wrong")
	t.Setenv("HTTPS_PROXY", "http://old")
	gemEnv := ChildEnv(s, gem)
	if !hasEnv(gemEnv, "GEMINI_CONFIG_DIR="+gem.ConfigDir) || hasEnv(gemEnv, "GEMINI_CONFIG_DIR=/wrong") {
		t.Fatalf("gemini env %v", gemEnv)
	}
	if !hasEnv(gemEnv, "HTTPS_PROXY=http://127.0.0.1:7890") {
		t.Fatalf("gemini proxy missing: %v", gemEnv)
	}
	grokEnv := ChildEnv(s, grk)
	if !hasEnv(grokEnv, "GROK_HOME="+grk.ConfigDir) || hasEnv(grokEnv, "GROK_HOME=/wrong") {
		t.Fatalf("grok env %v", grokEnv)
	}
}

func TestClaudeEnvCarriesOnlyTheAccountsOwnToken(t *testing.T) {
	t.Setenv("GCCCODE_HOME", t.TempDir())
	s := core.DefaultSettings()
	s.ClaudeHome = t.TempDir()
	tok := "sk-ant-oat01-" + strings.Repeat("a", 60)
	withToken := core.Account{ID: "cl-tok", Kind: core.KindClaude, ConfigDir: t.TempDir(), TokenAt: time.Now()}
	if err := claude.SaveToken(withToken.ID, tok); err != nil {
		t.Fatal(err)
	}
	keychain := core.Account{ID: "cl-key", Kind: core.KindClaude, ConfigDir: t.TempDir()}
	base := []string{"CLAUDE_CODE_OAUTH_TOKEN=from-zshrc", "PATH=/bin"}
	if env := claudeEnv(base, s, withToken); !hasEnv(env, "CLAUDE_CODE_OAUTH_TOKEN="+tok) || hasEnv(env, "CLAUDE_CODE_OAUTH_TOKEN=from-zshrc") {
		t.Fatalf("token account env %v", env)
	}
	for _, e := range claudeEnv(base, s, keychain) {
		if strings.HasPrefix(e, "CLAUDE_CODE_OAUTH_TOKEN=") {
			t.Fatalf("keychain account got a token: %s", e)
		}
	}
}

func TestPooledCodexIgnoresTheGatewayButKeepsOtherBaseURLs(t *testing.T) {
	t.Setenv("GCCCODE_HOME", t.TempDir())
	s := core.DefaultSettings()
	acc := core.Account{ID: "codex-1", Kind: core.KindCodex}
	viaGateway := codexEnv([]string{
		"OPENAI_BASE_URL=http://127.0.0.1:" + strings.Split(core.DefaultGatewayAddr, ":")[1] + "/v1",
		"OPENAI_API_KEY=gw-local", "PATH=/bin",
	}, s, acc)
	if hasEnv(viaGateway, "OPENAI_API_KEY=gw-local") || len(viaGateway) != 1 {
		t.Fatalf("gateway variables leaked into a pooled Codex: %v", viaGateway)
	}
	own := []string{"OPENAI_BASE_URL=https://relay.example.com/v1", "OPENAI_API_KEY=sk-own"}
	if got := codexEnv(own, s, acc); len(got) != 2 {
		t.Fatalf("the user's own base URL was dropped: %v", got)
	}
}

func TestFakeGeminiBinarySeesConfigDir(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GCCCODE_HOME", root)
	bin := filepath.Join(root, "fake-gemini")
	out := filepath.Join(root, "env.txt")
	script := "#!/bin/sh\nenv > " + out + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	profile := filepath.Join(root, "gemini", "gemini-1")
	os.MkdirAll(profile, 0o700)
	os.WriteFile(filepath.Join(profile, "google_accounts.json"), []byte(`{"email":"a@x.com"}`), 0o600)
	core.Mutate(func(s *core.Store) error {
		s.Settings.GeminiBin = bin
		s.Settings.GeminiHome = filepath.Join(root, "main-gemini")
		os.MkdirAll(s.Settings.GeminiHome, 0o755)
		s.Accounts = append(s.Accounts, core.Account{
			ID: "gemini-1", Kind: core.KindGemini, Label: "g1", ConfigDir: profile, CreatedAt: time.Now(),
		})
		return nil
	})
	if err := Run(Options{Kind: core.KindGemini, Account: "gemini-1"}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "GEMINI_CONFIG_DIR="+profile) {
		t.Fatalf("fake binary env:\n%s", b)
	}
}

func TestFakeGrokBinarySeesHome(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GCCCODE_HOME", root)
	bin := filepath.Join(root, "fake-grok")
	out := filepath.Join(root, "env.txt")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nenv > "+out+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	profile := filepath.Join(root, "grok", "grok-1")
	os.MkdirAll(profile, 0o700)
	os.WriteFile(filepath.Join(profile, "auth.json"), []byte(`{"default":{"email":"a@x.com","user_id":"u1"}}`), 0o600)
	core.Mutate(func(s *core.Store) error {
		s.Settings.GrokBin = bin
		s.Settings.GrokHome = filepath.Join(root, "main-grok")
		os.MkdirAll(s.Settings.GrokHome, 0o755)
		s.Accounts = append(s.Accounts, core.Account{
			ID: "grok-1", Kind: core.KindGrok, Label: "k1", ConfigDir: profile, CreatedAt: time.Now(),
		})
		return nil
	})
	if err := Run(Options{Kind: core.KindGrok, Account: "grok-1"}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "GROK_HOME="+profile) {
		t.Fatalf("fake binary env:\n%s", b)
	}
}

func hasEnv(env []string, want string) bool {
	for _, e := range env {
		if e == want {
			return true
		}
	}
	return false
}

func TestSetEnvReplacesAndRemoves(t *testing.T) {
	env := setEnv([]string{"A=1", LaunchEnv + "=parent", "B=2"}, LaunchEnv, "child")
	if len(env) != 3 || env[2] != LaunchEnv+"=child" {
		t.Fatalf("env = %v", env)
	}
	if env = setEnv(env, LaunchEnv, ""); len(env) != 2 {
		t.Fatalf("env = %v", env)
	}
}
