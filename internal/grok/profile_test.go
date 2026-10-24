package grok

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nearmetips/DailiCode/internal/core"
)

func TestEnsureProfileLinksSharedAndKeepsAuthPrivate(t *testing.T) {
	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, "skills"), 0o755)
	os.WriteFile(filepath.Join(home, "skills", "x.md"), []byte("skill"), 0o600)
	os.WriteFile(filepath.Join(home, "config.toml"), []byte("model = \"grok\"\n"), 0o600)
	os.WriteFile(filepath.Join(home, "auth.json"), []byte(`{"default":{"email":"a@x.com","user_id":"u1"}}`), 0o600)
	s := core.DefaultSettings()
	s.GrokHome = home
	dir := filepath.Join(t.TempDir(), "acct")
	if err := EnsureProfile(s, dir); err != nil {
		t.Fatal(err)
	}
	target, err := os.Readlink(filepath.Join(dir, "skills"))
	if err != nil || target != filepath.Join(home, "skills") {
		t.Fatalf("skills link = %s %v", target, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "sessions")); err != nil {
		t.Fatal("sessions dir missing")
	}
	cfg, _ := os.ReadFile(filepath.Join(dir, "config.toml"))
	if string(cfg) != "model = \"grok\"\n" {
		t.Fatalf("config not copied: %s", cfg)
	}
	if Ready(dir) {
		t.Fatal("auth must stay private to the profile, not copied from main")
	}
	os.WriteFile(filepath.Join(dir, "auth.json"), []byte(`{"default":{"email":"b@x.com","user_id":"u2"}}`), 0o600)
	if !Ready(dir) {
		t.Fatal("profile auth not ready")
	}
	idty, ok := ReadIdentity(dir)
	if !ok || idty.Email != "b@x.com" || idty.UserID != "u2" {
		t.Fatalf("identity %+v", idty)
	}
}

func TestProxyProblem(t *testing.T) {
	if ProxyProblem("socks5://127.0.0.1:1") == "" {
		t.Fatal("socks should be refused")
	}
	if ProxyProblem("127.0.0.1:7890") == "" {
		t.Fatal("scheme required")
	}
	if ProxyProblem("http://127.0.0.1:7890") != "" {
		t.Fatal("http should be ok")
	}
}
