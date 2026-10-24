package agy

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func tokenJSON(email, sub string) string {
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"email":"` + email + `","sub":"` + sub + `"}`))
	return `{"token":{"access_token":"at","refresh_token":"rt","expiry":"2026-10-12T05:47:00Z"},"auth_method":"consumer","id_token":"h.` + payload + `.s"}`
}

func noKeychain(t *testing.T, has bool) {
	t.Helper()
	old := keychainHas
	keychainHas = func(string) bool { return has }
	t.Cleanup(func() { keychainHas = old })
}

func TestReadIdentityFromTheTokenFile(t *testing.T) {
	noKeychain(t, false)
	home := t.TempDir()
	if Ready(home) {
		t.Fatal("an empty home has no login")
	}
	os.MkdirAll(filepath.Join(home, ".gemini"), 0o700)
	os.WriteFile(TokenPath(home), []byte(tokenJSON("a@x.com", "42")), 0o600)
	idty, ok := ReadIdentity(home)
	if !ok || idty.Email != "a@x.com" || idty.UserID != "42" {
		t.Fatalf("identity %+v %v", idty, ok)
	}
	os.WriteFile(TokenPath(home), []byte(`{"token":{},"id_token":""}`), 0o600)
	if Ready(home) {
		t.Fatal("a token file without tokens is not a login")
	}
}

func TestKeychainLoginTakesTheEmailFromTheLastSignIn(t *testing.T) {
	noKeychain(t, true)
	home := t.TempDir()
	logs := filepath.Join(cliDir(home), "log")
	os.MkdirAll(logs, 0o755)
	line := func(email string) string {
		return fmt.Sprintf("I1012 12:44:15.000000       1 server_oauth.go:190] applyAuthResult: email=%s, authMethod=consumer, quotaProject=\n", email)
	}
	old := filepath.Join(logs, "cli-old.log")
	os.WriteFile(old, []byte(line("old@x.com")), 0o600)
	os.Chtimes(old, time.Now().Add(-time.Hour), time.Now().Add(-time.Hour))
	os.WriteFile(filepath.Join(logs, "cli-new.log"), []byte("no sign-in in this run\n"+line("new@x.com")), 0o600)
	os.MkdirAll(filepath.Join(home, ".gemini"), 0o700)
	os.WriteFile(TokenPath(home), []byte(tokenJSON("ide@x.com", "7")), 0o600)

	idty, ok := ReadIdentity(home)
	if !ok || idty.Email != "new@x.com" || idty.UserID != "" {
		t.Fatalf("the keychain login's last sign-in should win over another account's token file: %+v %v", idty, ok)
	}
	os.Remove(TokenPath(home))
	if !Ready(home) {
		t.Fatal("a keychain login without the token file is still a login")
	}
}

func TestAgyProxyProblem(t *testing.T) {
	for proxy, ok := range map[string]bool{
		"": true, "http://127.0.0.1:7890": true, "socks5://127.0.0.1:1080": true,
		"127.0.0.1:7890": false, "socks4://127.0.0.1:1080": false,
	} {
		if got := ProxyProblem(proxy) == ""; got != ok {
			t.Errorf("ProxyProblem(%q) ok=%v, want %v", proxy, got, ok)
		}
	}
}
