package claude

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nearmetips/DailiCode/internal/core"
)

func writeDevice(t *testing.T, dir, machine, user string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"machineID":"` + machine + `","userID":"` + user + `","theme":"light"}`)
	if err := os.WriteFile(filepath.Join(dir, ".claude.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSeparateDevicesKeepsTheOriginal(t *testing.T) {
	home := t.TempDir()
	copyDir := t.TempDir()
	writeDevice(t, home, "machine-a", "user-a")
	writeDevice(t, copyDir, "machine-a", "user-a")
	s := core.DefaultSettings()
	s.ClaudeHome = home
	accounts := []core.Account{
		{ID: "main", Kind: core.KindClaude, ConfigDir: home, CreatedAt: time.Now()},
		{ID: "copy", Kind: core.KindClaude, ConfigDir: copyDir, CreatedAt: time.Now().Add(time.Minute)},
	}
	cleared, err := SeparateDevices(s, accounts)
	if err != nil {
		t.Fatal(err)
	}
	if len(cleared) != 1 || cleared[0] != "copy" {
		t.Fatalf("cleared %v", cleared)
	}
	if got := readDevice(filepath.Join(home, ".claude.json")); got.MachineID != "machine-a" {
		t.Fatalf("main device changed: %+v", got)
	}
	if got := readDevice(filepath.Join(copyDir, ".claude.json")); got.MachineID != "" || got.UserID != "" {
		t.Fatalf("copy still linked: %+v", got)
	}
	b, _ := os.ReadFile(filepath.Join(copyDir, ".claude.json"))
	if !contains(string(b), "light") {
		t.Fatalf("other settings dropped: %s", b)
	}
}

func bareSettings(t *testing.T) core.Settings {
	t.Helper()
	s := core.DefaultSettings()
	s.ClaudeHome = t.TempDir()
	return s
}

func TestEgressProblem(t *testing.T) {
	s := bareSettings(t)
	accounts := []core.Account{
		{ID: "a", Kind: core.KindClaude, Proxy: "http://a:1"},
		{ID: "b", Kind: core.KindClaude},
		{ID: "c", Kind: core.KindClaude, Proxy: "http://a:1"},
	}
	if EgressProblem(s, accounts, "a") == "" || EgressProblem(s, accounts, "c") == "" {
		t.Fatal("two accounts on one proxy must be refused")
	}
	if EgressProblem(s, accounts, "b") == "" {
		t.Fatal("empty proxy with siblings must be refused")
	}
	if EgressProblem(s, accounts[:1], "a") != "" {
		t.Fatal("a single account needs no proxy")
	}
}

func TestEgressRefusesSocksAndSameProxySpelledTwice(t *testing.T) {
	s := bareSettings(t)
	socks := []core.Account{{ID: "a", Kind: core.KindClaude, Proxy: "socks5://127.0.0.1:7891"}}
	if EgressProblem(s, socks, "a") == "" {
		t.Fatal("a SOCKS proxy must be refused even for a single account: Claude Code ignores it")
	}
	same := []core.Account{
		{ID: "a", Kind: core.KindClaude, Proxy: "http://localhost:7890/"},
		{ID: "b", Kind: core.KindClaude, Proxy: "HTTP://127.0.0.1:7890"},
	}
	if EgressProblem(s, same, "a") == "" {
		t.Fatal("one proxy written two ways must count as one egress")
	}
	residential := []core.Account{
		{ID: "a", Kind: core.KindClaude, Proxy: "http://user-1:pw@gate.example:8000"},
		{ID: "b", Kind: core.KindClaude, Proxy: "http://user-2:pw@gate.example:8000"},
	}
	if msg := EgressProblem(s, residential, "a"); msg != "" {
		t.Fatalf("different usernames on one gateway are different exits: %s", msg)
	}
	for _, p := range []string{"127.0.0.1:7890", "localhost:7890", "ftp://x:1"} {
		if ProxyProblem(p) == "" {
			t.Fatalf("%q should be refused", p)
		}
	}
	if ProxyProblem("https://proxy.example:443") != "" {
		t.Fatal("an https proxy is fine")
	}
}

func TestSettingsProxyIsNotCopiedIntoAccounts(t *testing.T) {
	home := t.TempDir()
	os.WriteFile(filepath.Join(home, "settings.json"), []byte(`{"env":{"HTTPS_PROXY":"http://127.0.0.1:7890","NO_PROXY":"localhost"}}`), 0o600)
	s := core.DefaultSettings()
	s.ClaudeHome = home
	dir := filepath.Join(t.TempDir(), "acct")
	if err := EnsureProfile(s, dir); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "settings.json"))
	if contains(string(b), "7890") {
		t.Fatalf("the shared proxy would override the account's own: %s", b)
	}
	if !contains(string(b), "NO_PROXY") {
		t.Fatalf("unrelated env dropped: %s", b)
	}

	main := core.Account{ID: "main", Kind: core.KindClaude, ConfigDir: home}
	plain := core.Account{ID: "plain", Kind: core.KindClaude, ConfigDir: dir}
	own := core.Account{ID: "own", Kind: core.KindClaude, ConfigDir: dir + "-2", Proxy: "http://127.0.0.1:7891"}
	if got := EffectiveProxy(s, plain); got != "http://127.0.0.1:7890" {
		t.Fatalf("an account without its own proxy keeps the shared one, got %q", got)
	}
	if got := EffectiveProxy(s, own); got != "http://127.0.0.1:7891" {
		t.Fatalf("an account's own proxy wins, got %q", got)
	}
	if EgressProblem(s, []core.Account{main, plain}, "plain") == "" {
		t.Fatal("main and plain both leave through settings.json's proxy")
	}
	if msg := EgressProblem(s, []core.Account{main, own}, "own"); msg != "" {
		t.Fatalf("separate egress refused: %s", msg)
	}
	main.Proxy = "http://127.0.0.1:7892"
	if msg := EgressProblem(s, []core.Account{main}, "main"); !contains(msg, "settings.json") {
		t.Fatalf("main home's settings.json proxy overrides its account proxy; got %q", msg)
	}
}

func TestProfileSessionEnvIsPrivate(t *testing.T) {
	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, "session-env"), 0o755)
	os.WriteFile(filepath.Join(home, "settings.json"), []byte(`{}`), 0o600)
	s := core.DefaultSettings()
	s.ClaudeHome = home
	dir := filepath.Join(t.TempDir(), "acct")
	if err := EnsureProfile(s, dir); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(filepath.Join(dir, "session-env"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Fatal("session-env is shared")
	}
}
