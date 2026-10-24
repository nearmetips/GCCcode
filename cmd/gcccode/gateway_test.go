package main

import (
	"strings"
	"testing"

	"github.com/nearmetips/DailiCode/internal/core"
	"github.com/nearmetips/DailiCode/internal/gateway"
)

func TestGatewayProviderLifecycle(t *testing.T) {
	t.Setenv("GCCCODE_HOME", t.TempDir())

	if err := providerAdd([]string{"deepseek", "--url", "https://api.deepseek.com/v1", "--key", "sk-1", "--model", "deepseek-chat"}); err != nil {
		t.Fatal(err)
	}
	if err := providerAdd([]string{"backup", "--url", "https://relay.example/v1", "--key", "sk-2"}); err != nil {
		t.Fatal(err)
	}
	if err := providerAdd([]string{"backup", "--url", "https://x/v1", "--key", "sk-3"}); err == nil {
		t.Fatal("a duplicate name was accepted")
	}
	if err := providerAdd([]string{"bad", "--url", "not-a-url", "--key", "k"}); err == nil {
		t.Fatal("a malformed url was accepted")
	}
	if err := providerAdd([]string{"nokey", "--url", "https://x/v1"}); err == nil {
		t.Fatal("a provider without a key was accepted")
	}
	if err := providerAdd([]string{"--url", "https://x/v1"}); err == nil {
		t.Fatal("a provider without a name was accepted")
	}

	st, _ := core.Load()
	ps := st.Settings.Gateway.Providers
	if len(ps) != 2 || ps[0].Name != "deepseek" || ps[0].Model != "deepseek-chat" || ps[1].Name != "backup" {
		t.Fatalf("providers %+v", ps)
	}

	if err := providerChange("disable", "deepseek"); err != nil {
		t.Fatal(err)
	}
	st, _ = core.Load()
	if !st.Settings.Gateway.Providers[0].Disabled {
		t.Fatal("not disabled")
	}
	if err := providerChange("enable", "deepseek"); err != nil {
		t.Fatal(err)
	}
	if err := providerChange("rm", "deepseek"); err != nil {
		t.Fatal(err)
	}
	st, _ = core.Load()
	if ps := st.Settings.Gateway.Providers; len(ps) != 1 || ps[0].Name != "backup" {
		t.Fatalf("after rm: %+v", ps)
	}
	if err := providerChange("rm", "nope"); err == nil {
		t.Fatal("removing a missing provider succeeded")
	}
}

func TestGatewayTokenIsCreatedOnceAndAddrSaved(t *testing.T) {
	t.Setenv("GCCCODE_HOME", t.TempDir())
	st, err := ensureGatewayToken("127.0.0.1:47310", true)
	if err != nil {
		t.Fatal(err)
	}
	first := st.Settings.Gateway.Token
	if !strings.HasPrefix(first, "gw-") || len(first) < 30 {
		t.Fatalf("token %q", first)
	}
	st, _ = ensureGatewayToken("127.0.0.1:47399", false)
	if st.Settings.Gateway.Token != first {
		t.Fatal("token changed on the second call")
	}
	if st.Settings.Gateway.Addr != "127.0.0.1:47310" {
		t.Fatalf("addr %q", st.Settings.Gateway.Addr)
	}
}

func TestGatewayAddrMustBeLoopback(t *testing.T) {
	for _, bad := range []string{"0.0.0.0:3426", "192.168.0.2:3426", "example.com:3426", "3426"} {
		if checkLoopbackAddr(bad) == nil {
			t.Fatalf("%s accepted", bad)
		}
	}
	if err := checkLoopbackAddr("127.0.0.1:3426"); err != nil {
		t.Fatal(err)
	}
	if err := checkLoopbackAddr("[::1]:3426"); err != nil {
		t.Fatal(err)
	}
}

func TestGatewayDoctorWarnsWhenTheShellBypassesThePool(t *testing.T) {
	t.Setenv("GCCCODE_HOME", t.TempDir())
	env := func(kv map[string]string) func(string) string {
		return func(k string) string { return kv[k] }
	}
	st, _ := core.Load()
	if lines := gatewayDoctor(st, env(nil)); lines != nil {
		t.Fatalf("an unused gateway should stay quiet: %v", lines)
	}

	providerAdd([]string{"a", "--url", "https://x/v1", "--key", "k"})
	st, _ = core.Load()
	lines := gatewayDoctor(st, env(nil))
	if len(lines) != 1 || !strings.Contains(lines[0], "没有运行") {
		t.Fatalf("%v", lines)
	}
	lines = gatewayDoctor(st, env(map[string]string{
		"OPENAI_BASE_URL":    "http://" + core.DefaultGatewayAddr + "/v1",
		"ANTHROPIC_BASE_URL": "http://localhost:3426",
	}))
	if len(lines) != 2 || !strings.Contains(lines[1], "连不上") ||
		!strings.Contains(lines[1], "OPENAI_BASE_URL、ANTHROPIC_BASE_URL") ||
		!strings.Contains(lines[1], "unset OPENAI_BASE_URL OPENAI_API_KEY ANTHROPIC_BASE_URL ANTHROPIC_AUTH_TOKEN") {
		t.Fatalf("env points at a stopped gateway: %v", lines)
	}
	if lines := gatewayDoctor(st, env(map[string]string{
		"OPENAI_BASE_URL":        "https://api.openai.com/v1",
		"GOOGLE_GEMINI_BASE_URL": "http://127.0.0.1:9999",
	})); len(lines) != 1 {
		t.Fatalf("unrelated base URLs must not warn: %v", lines)
	}
}

func TestUseGatewayOnOff(t *testing.T) {
	t.Setenv("GCCCODE_HOME", t.TempDir())
	spawned := 0
	old := spawnGateway
	t.Cleanup(func() { spawnGateway = old })
	spawnGateway = func(addr string) (gateway.Process, error) {
		spawned++
		if err := gateway.WritePID(addr); err != nil {
			return gateway.Process{}, err
		}
		p, _ := gateway.Running()
		return p, nil
	}
	use := func() []string {
		st, _ := core.Load()
		return st.Settings.Gateway.Use
	}

	if err := cmdUseGateway([]string{"on"}); err == nil || spawned != 0 {
		t.Fatalf("on with no upstream: %v, spawned %d", err, spawned)
	}
	providerAdd([]string{"claude-api", "--protocol", "anthropic", "--url", "https://api.anthropic.com", "--key", "k"})
	providerAdd([]string{"gemini-api", "--protocol", "gemini", "--url", "https://generativelanguage.googleapis.com", "--key", "k"})
	if err := cmdUseGateway([]string{"on", "codex"}); err == nil || !strings.Contains(err.Error(), "--responses") {
		t.Fatalf("codex without a Responses upstream: %v", err)
	}
	if err := cmdUseGateway([]string{"on", "grok"}); err == nil || !strings.Contains(err.Error(), "Grok") {
		t.Fatalf("grok: %v", err)
	}
	if err := cmdUseGateway([]string{"on"}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(use(), ","); got != "claude,gemini" || spawned != 1 {
		t.Fatalf("use=%s spawned=%d", got, spawned)
	}
	if err := cmdUseGateway([]string{"off", "gemini"}); err != nil {
		t.Fatal(err)
	}
	if err := cmdUseGateway([]string{"on", "claude"}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(use(), ","); got != "claude" || spawned != 1 {
		t.Fatalf("use=%s spawned=%d (a running gateway was started again?)", got, spawned)
	}

	providerChange("disable", "claude-api")
	st, _ := core.Load()
	if lines := useGatewayLines(st); !strings.Contains(lines[0], "启动会失败") || !strings.Contains(lines[1], "直连官方") {
		t.Fatalf("%v", lines)
	}
	if err := cmdUseGateway([]string{"off"}); err != nil {
		t.Fatal(err)
	}
	if len(use()) != 0 {
		t.Fatalf("off left %v", use())
	}
}

func TestProviderAddSubscription(t *testing.T) {
	t.Setenv("GCCCODE_HOME", t.TempDir())
	// No account of the kind yet: refused.
	if err := providerAdd([]string{"codexpool", "--subscription", "codex"}); err == nil {
		t.Fatal("codex subscription accepted with no Codex account")
	}
	// A URL alongside a subscription is rejected by validation.
	if err := providerAdd([]string{"x", "--subscription", "codex", "--url", "https://x/v1"}); err == nil {
		t.Fatal("subscription with a URL accepted")
	}
	// An unknown subscription kind is rejected.
	if err := providerAdd([]string{"x", "--subscription", "grok"}); err == nil {
		t.Fatal("unknown subscription kind accepted")
	}

	_, _ = core.Mutate(func(s *core.Store) error {
		s.Accounts = append(s.Accounts,
			core.Account{ID: "codex-1", Kind: core.KindCodex},
			core.Account{ID: "claude-1", Kind: core.KindClaude, ConfigDir: "/a"},
			core.Account{ID: "claude-2", Kind: core.KindClaude, ConfigDir: "/b"},
		)
		return nil
	})

	if err := providerAdd([]string{"codexpool", "--subscription", "codex"}); err != nil {
		t.Fatalf("codex subscription: %v", err)
	}
	// A second subscription of the same kind is refused.
	if err := providerAdd([]string{"codex2", "--subscription", "codex"}); err == nil {
		t.Fatal("two codex subscriptions accepted")
	}

	// Two Claude accounts without distinct proxies must be refused.
	if err := providerAdd([]string{"claudepool", "--subscription", "claude"}); err == nil {
		t.Fatal("claude subscription accepted without distinct proxies")
	}
	_, _ = core.Mutate(func(s *core.Store) error {
		for i := range s.Accounts {
			switch s.Accounts[i].ID {
			case "claude-1":
				s.Accounts[i].Proxy = "http://127.0.0.1:1"
			case "claude-2":
				s.Accounts[i].Proxy = "http://127.0.0.1:2"
			}
		}
		return nil
	})
	if err := providerAdd([]string{"claudepool", "--subscription", "claude"}); err != nil {
		t.Fatalf("claude subscription with distinct proxies: %v", err)
	}

	st, _ := core.Load()
	var codex, claude bool
	for _, p := range st.Settings.Gateway.Providers {
		switch p.Subscription {
		case "codex":
			codex = true
		case "claude":
			claude = true
		}
	}
	if !codex || !claude {
		t.Fatalf("subscription providers missing: %+v", st.Settings.Gateway.Providers)
	}
}

func TestProviderAddProtocols(t *testing.T) {
	t.Setenv("GCCCODE_HOME", t.TempDir())
	for _, args := range [][]string{
		{"claude-api", "--protocol", "anthropic", "--url", "https://api.anthropic.com", "--key", "sk-ant"},
		{"gemini-api", "--protocol", "gemini", "--url", "https://generativelanguage.googleapis.com", "--key", "AIza"},
		{"openai", "--protocol", "openai", "--responses", "--url", "https://api.openai.com/v1", "--key", "sk"},
	} {
		if err := providerAdd(args); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	for _, args := range [][]string{
		{"x", "--protocol", "azure", "--url", "https://x/v1", "--key", "k"},
		{"x", "--protocol", "anthropic", "--responses", "--url", "https://api.anthropic.com", "--key", "k"},
		{"x", "--protocol", "anthropic", "--url", "https://api.anthropic.com/v1", "--key", "k"},
	} {
		if providerAdd(args) == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	st, _ := core.Load()
	ps := st.Settings.Gateway.Providers
	if len(ps) != 3 || ps[0].Protocol != "anthropic" || ps[1].Protocol != "gemini" || ps[2].Protocol != "" || !ps[2].Responses {
		t.Fatalf("%+v", ps)
	}
}
