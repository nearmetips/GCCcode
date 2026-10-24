package gemini

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nearmetips/DailiCode/internal/core"
)

func TestEnsureProfileLinksSharedAndStripsProxyEnv(t *testing.T) {
	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, "extensions"), 0o755)
	os.WriteFile(filepath.Join(home, "extensions", "x"), []byte("x"), 0o600)
	os.WriteFile(filepath.Join(home, "settings.json"), []byte(`{"theme":"dark","env":{"HTTPS_PROXY":"http://proxy","KEEP":"1"}}`), 0o600)
	s := core.DefaultSettings()
	s.GeminiHome = home
	dir := filepath.Join(t.TempDir(), "acct")
	if err := EnsureProfile(s, dir); err != nil {
		t.Fatal(err)
	}
	target, err := os.Readlink(filepath.Join(dir, "extensions"))
	if err != nil || target != filepath.Join(home, "extensions") {
		t.Fatalf("extensions link = %s %v", target, err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "settings.json"))
	text := string(b)
	if strings.Contains(text, "HTTPS_PROXY") {
		t.Fatalf("proxy leaked: %s", text)
	}
	if !strings.Contains(text, "KEEP") || !strings.Contains(text, "dark") {
		t.Fatalf("kept settings missing: %s", text)
	}
	os.WriteFile(filepath.Join(dir, "google_accounts.json"), []byte(`{"email":"g@x.com","id":"gid"}`), 0o600)
	if !Ready(dir) {
		t.Fatal("not ready after oauth file")
	}
	idty, ok := ReadIdentity(dir)
	if !ok || idty.Email != "g@x.com" {
		t.Fatalf("identity %+v", idty)
	}
}

func TestReadyAcceptsAPIKeyEnv(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".env"), []byte("GEMINI_API_KEY=abc\n"), 0o600)
	if !Ready(dir) {
		t.Fatal("api key should count as ready")
	}
}

func TestProxyProblem(t *testing.T) {
	if ProxyProblem("socks5://127.0.0.1:1") == "" {
		t.Fatal("socks should be refused")
	}
	if ProxyProblem("http://127.0.0.1:7890") != "" {
		t.Fatal("http should be ok")
	}
}
