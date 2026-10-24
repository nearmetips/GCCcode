package launcher

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nearmetips/DailiCode/internal/core"
	"github.com/nearmetips/DailiCode/internal/gateway"
)

const fakeGatewayAddr = "127.0.0.1:5999"

// gatewayRig is a sandbox with use-gateway on for kind and a gateway the pid
// file says is running (this test process).
func gatewayRig(t *testing.T, kind string, providers ...core.GatewayProvider) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("GCCCODE_HOME", root)
	t.Setenv("GEMINI_CONFIG_DIR", "")
	if err := gateway.WritePID(fakeGatewayAddr); err != nil {
		t.Fatal(err)
	}
	_, err := core.Mutate(func(s *core.Store) error {
		s.Settings.ClaudeHome = filepath.Join(root, "main-claude")
		s.Settings.GeminiHome = filepath.Join(root, "main-gemini")
		s.Settings.Gateway.Token = "gw-test"
		s.Settings.Gateway.Use = []string{kind}
		s.Settings.Gateway.Providers = providers
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// fakeClient writes a script that records its environment and arguments.
func fakeClient(t *testing.T, root, name string) (bin string, env func() string, args func() []string) {
	t.Helper()
	bin = filepath.Join(root, "fake-"+name)
	envFile, argsFile := filepath.Join(root, name+".env"), filepath.Join(root, name+".args")
	script := "#!/bin/sh\nenv > " + envFile + "\nfor a in \"$@\"; do echo \"$a\"; done > " + argsFile + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	read := func(p string) string {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	return bin, func() string { return read(envFile) }, func() []string {
		return strings.Split(strings.TrimRight(read(argsFile), "\n"), "\n")
	}
}

func setBin(t *testing.T, f func(*core.Settings)) {
	t.Helper()
	if _, err := core.Mutate(func(s *core.Store) error { f(&s.Settings); return nil }); err != nil {
		t.Fatal(err)
	}
}

func envHas(env, kv string) bool {
	for _, line := range strings.Split(env, "\n") {
		if line == kv {
			return true
		}
	}
	return false
}

var openAIUpstream = core.GatewayProvider{Name: "ds", BaseURL: "https://api.deepseek.com/v1", APIKey: "k"}

func TestUseGatewayStartsClaudeOnTheGatewayProfile(t *testing.T) {
	root := gatewayRig(t, core.KindClaude, openAIUpstream)
	bin, env, args := fakeClient(t, root, "claude")
	setBin(t, func(s *core.Settings) { s.ClaudeBin = bin })
	t.Setenv("ANTHROPIC_BASE_URL", "https://elsewhere.example")
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-own")
	t.Setenv("NO_PROXY", "corp.example")

	if err := Run(Options{Kind: core.KindClaude, Args: []string{"-p", "hi"}, Stderr: os.Stderr}); err != nil {
		t.Fatal(err)
	}
	e := env()
	dir := core.GatewayProfileDir(core.KindClaude)
	for _, want := range []string{
		"CLAUDE_CONFIG_DIR=" + dir,
		"ANTHROPIC_BASE_URL=http://" + fakeGatewayAddr,
		"ANTHROPIC_AUTH_TOKEN=gw-test",
		"NO_PROXY=corp.example,127.0.0.1,localhost,::1",
	} {
		if !envHas(e, want) {
			t.Errorf("missing %s in\n%s", want, e)
		}
	}
	if strings.Contains(e, "elsewhere.example") || strings.Contains(e, "sk-ant-own") {
		t.Fatalf("the shell's own Anthropic settings leaked in:\n%s", e)
	}
	if got := args(); strings.Join(got, " ") != "-p hi" {
		t.Fatalf("args %v", got)
	}
	var cfg map[string]any
	b, _ := os.ReadFile(filepath.Join(dir, ".claude.json"))
	if json.Unmarshal(b, &cfg) != nil || cfg["hasCompletedOnboarding"] != true {
		t.Fatalf("profile not marked onboarded: %s", b)
	}
	if fi, err := os.Lstat(filepath.Join(dir, "projects")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("the gateway profile does not share session history")
	}
	st, _ := core.Load()
	if len(st.State.Occupied) != 0 {
		t.Fatalf("a gateway launch occupied an account: %v", st.State.Occupied)
	}
}

func TestUseGatewayIsSkippedForAnExplicitAccount(t *testing.T) {
	gatewayRig(t, core.KindClaude, openAIUpstream)
	err := Run(Options{Kind: core.KindClaude, Account: "claude-9", Stderr: os.Stderr})
	if err == nil || !strings.Contains(err.Error(), "没有这个 claude 账号") {
		t.Fatalf("--account should go to the pool: %v", err)
	}
}

func TestUseGatewayStartsCodexWithAProviderOfItsOwn(t *testing.T) {
	p := openAIUpstream
	p.Responses = true
	root := gatewayRig(t, core.KindCodex, p)
	bin, env, args := fakeClient(t, root, "codex")
	setBin(t, func(s *core.Settings) { s.CodexBin = bin })
	t.Setenv("OPENAI_BASE_URL", "http://"+fakeGatewayAddr+"/v1")
	t.Setenv("OPENAI_API_KEY", "gw-test")

	if err := Run(Options{Kind: core.KindCodex, Resume: "sess-1", Args: []string{"--full-auto"}, Stderr: os.Stderr}); err != nil {
		t.Fatal(err)
	}
	e := env()
	if !envHas(e, codexGatewayKeyEnv+"=gw-test") || strings.Contains(e, "OPENAI_BASE_URL=") || strings.Contains(e, "OPENAI_API_KEY=") {
		t.Fatalf("env:\n%s", e)
	}
	got := strings.Join(args(), " ")
	want := strings.Join(codexGatewayFlags(fakeGatewayAddr), " ") + " resume sess-1 --full-auto"
	if got != want {
		t.Fatalf("args\n got %s\nwant %s", got, want)
	}
	if !strings.Contains(got, `base_url="http://`+fakeGatewayAddr+`/v1"`) {
		t.Fatalf("base url missing: %s", got)
	}
}

func TestUseGatewayStartsGeminiWithAPIKeyAuth(t *testing.T) {
	root := gatewayRig(t, core.KindGemini, core.GatewayProvider{Name: "g", BaseURL: "https://generativelanguage.googleapis.com", APIKey: "AIza", Protocol: gateway.ProtoGemini})
	bin, env, _ := fakeClient(t, root, "gemini")
	setBin(t, func(s *core.Settings) { s.GeminiBin = bin })
	t.Setenv("GEMINI_API_KEY", "AIza-own")

	if err := Run(Options{Kind: core.KindGemini, Stderr: os.Stderr}); err != nil {
		t.Fatal(err)
	}
	e := env()
	dir := core.GatewayProfileDir(core.KindGemini)
	for _, want := range []string{
		"GEMINI_CONFIG_DIR=" + dir,
		"GOOGLE_GEMINI_BASE_URL=http://" + fakeGatewayAddr,
		"GEMINI_API_KEY=gw-test",
		"GEMINI_DEFAULT_AUTH_TYPE=gemini-api-key",
	} {
		if !envHas(e, want) {
			t.Errorf("missing %s in\n%s", want, e)
		}
	}
	b, _ := os.ReadFile(filepath.Join(dir, "settings.json"))
	if !strings.Contains(string(b), `"selectedType": "gemini-api-key"`) {
		t.Fatalf("settings.json: %s", b)
	}
}

func TestUseGatewayWithoutAMatchingUpstreamSaysHowToGoBack(t *testing.T) {
	gatewayRig(t, core.KindClaude, core.GatewayProvider{Name: "g", BaseURL: "https://x.example.com", APIKey: "k", Protocol: gateway.ProtoGemini})
	err := Run(Options{Kind: core.KindClaude, Stderr: os.Stderr})
	if err == nil || !strings.Contains(err.Error(), "use-gateway off claude") {
		t.Fatalf("%v", err)
	}
}

func TestUseGatewayStartsAStoppedGateway(t *testing.T) {
	root := gatewayRig(t, core.KindClaude, openAIUpstream)
	os.Remove(core.GatewayPIDFile())
	bin, env, _ := fakeClient(t, root, "claude")
	setBin(t, func(s *core.Settings) { s.ClaudeBin = bin })

	old := EnsureGateway
	t.Cleanup(func() { EnsureGateway = old })
	EnsureGateway = nil
	if err := Run(Options{Kind: core.KindClaude, Stderr: os.Stderr}); err == nil || !strings.Contains(err.Error(), "没有在运行") {
		t.Fatalf("no starter: %v", err)
	}
	started := 0
	EnsureGateway = func() (gateway.Process, error) {
		started++
		if err := gateway.WritePID("127.0.0.1:6001"); err != nil {
			return gateway.Process{}, err
		}
		p, _ := gateway.Running()
		return p, nil
	}
	if err := Run(Options{Kind: core.KindClaude, Stderr: os.Stderr}); err != nil {
		t.Fatal(err)
	}
	if started != 1 || !envHas(env(), "ANTHROPIC_BASE_URL=http://127.0.0.1:6001") {
		t.Fatalf("started=%d env:\n%s", started, env())
	}
}

func TestNoProxyLoopbackKeepsWhatIsThere(t *testing.T) {
	got := noProxyLoopback([]string{"no_proxy=localhost, corp", "PATH=/bin"})
	if !hasEnv(got, "no_proxy=localhost,corp,127.0.0.1,::1") || !hasEnv(got, "NO_PROXY=127.0.0.1,localhost,::1") {
		t.Fatalf("%v", got)
	}
}
