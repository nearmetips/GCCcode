package main

import (
	"os"
	"strings"
	"testing"

	"github.com/nearmetips/DailiCode/internal/browser"
	"github.com/nearmetips/DailiCode/internal/core"
	"github.com/nearmetips/DailiCode/internal/setuptoken"
)

func envValue(env []string, key string) (string, bool) {
	for _, e := range env {
		if k, v, ok := strings.Cut(e, "="); ok && k == key {
			return v, true
		}
	}
	return "", false
}

func TestSetupTokenOpensTheAccountsOwnBrowser(t *testing.T) {
	t.Setenv("GCCCODE_HOME", t.TempDir())
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "from-zshrc")
	t.Setenv("BROWSER", "firefox")
	s := core.DefaultSettings()
	s.ClaudeHome = t.TempDir()
	acc := core.Account{ID: "cl-2", Kind: core.KindClaude, ConfigDir: t.TempDir(), Proxy: "http://127.0.0.1:7891"}

	env := setuptoken.Env(s, acc, true)
	self, _ := os.Executable()
	if v, _ := envValue(env, "BROWSER"); v != self {
		t.Fatalf("BROWSER=%q, want gcccode itself", v)
	}
	if v, _ := envValue(env, browser.ForEnv); v != "cl-2" {
		t.Fatalf("%s=%q", browser.ForEnv, v)
	}
	if v, _ := envValue(env, setuptoken.ProxyEnv); v != acc.Proxy {
		t.Fatalf("browser proxy %q, want the account's", v)
	}
	if _, ok := envValue(env, "CLAUDE_CODE_OAUTH_TOKEN"); ok {
		t.Fatal("setup-token must not run with a token already set")
	}

	env = setuptoken.Env(s, acc, false)
	if v, _ := envValue(env, "BROWSER"); v != setuptoken.NoBrowser {
		t.Fatalf("without a browser of its own, BROWSER=%q must not open the default one", v)
	}
	if _, ok := envValue(env, browser.ForEnv); ok {
		t.Fatal("no account browser to route to")
	}
}
