package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/nearmetips/DailiCode/internal/core"
)

func TestKeychainServiceDiffersPerDir(t *testing.T) {
	a := KeychainService("/tmp/gcccode/claude/one")
	b := KeychainService("/tmp/gcccode/claude/two")
	if a == b || a == "Claude Code-credentials" {
		t.Fatalf("services not isolated: %s %s", a, b)
	}
}

func TestProfileLinksHistoryAndStripsRelay(t *testing.T) {
	home := t.TempDir()
	os.WriteFile(filepath.Join(home, "settings.json"), []byte(`{"theme":"light","env":{"ANTHROPIC_BASE_URL":"https://relay.example","ANTHROPIC_AUTH_TOKEN":"sk-test","KEEP":"1"}}`), 0o600)
	os.MkdirAll(filepath.Join(home, "projects"), 0o755)
	os.WriteFile(filepath.Join(home, "history.jsonl"), []byte("{\"id\":1}\n"), 0o600)
	s := core.DefaultSettings()
	s.ClaudeHome = home
	dir := filepath.Join(t.TempDir(), "acct")
	if err := EnsureProfile(s, dir); err != nil {
		t.Fatal(err)
	}
	target, err := os.Readlink(filepath.Join(dir, "projects"))
	if err != nil || target != filepath.Join(home, "projects") {
		t.Fatalf("projects link = %s %v", target, err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "settings.json"))
	text := string(b)
	if contains(text, "ANTHROPIC_BASE_URL") || contains(text, "sk-test") {
		t.Fatalf("relay settings leaked: %s", text)
	}
	if !contains(text, "KEEP") || !contains(text, "light") {
		t.Fatalf("kept settings missing: %s", text)
	}
	link := filepath.Join(dir, "history.jsonl")
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(link, []byte("{\"id\":2}\n"), 0o600)
	if err := EnsureProfile(s, dir); err != nil {
		t.Fatal(err)
	}
	merged, _ := os.ReadFile(filepath.Join(home, "history.jsonl"))
	if !contains(string(merged), `"id":1`) || !contains(string(merged), `"id":2`) {
		t.Fatalf("history not merged: %s", merged)
	}
}

func TestSyncMCPDropsServersRemovedFromMain(t *testing.T) {
	home := t.TempDir()
	mainCfg := filepath.Join(home, ".claude.json")
	os.WriteFile(mainCfg, []byte(`{"mcpServers":{"lcu":{"command":"/lcu"},"docs":{"command":"/docs"}}}`), 0o600)
	s := core.DefaultSettings()
	s.ClaudeHome = home
	dir := filepath.Join(t.TempDir(), "acct")
	if err := EnsureProfile(s, dir); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, ".claude.json")
	b, _ := os.ReadFile(cfg)
	var prof map[string]any
	json.Unmarshal(b, &prof)
	prof["mcpServers"].(map[string]any)["mine"] = map[string]any{"command": "/mine"}
	prof["oauthAccount"] = map[string]any{"accountUuid": "u-1"}
	b, _ = json.Marshal(prof)
	os.WriteFile(cfg, b, 0o600)

	os.WriteFile(mainCfg, []byte(`{"mcpServers":{"docs":{"command":"/docs"}}}`), 0o600)
	if err := EnsureProfile(s, dir); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(cfg)
	text := string(b)
	if contains(text, `"lcu"`) {
		t.Fatalf("server removed from the main config is still in the account: %s", text)
	}
	if !contains(text, `"docs"`) || !contains(text, `"mine"`) || !contains(text, "u-1") {
		t.Fatalf("kept entries missing: %s", text)
	}

	os.WriteFile(mainCfg, []byte(`{}`), 0o600)
	if err := EnsureProfile(s, dir); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(cfg)
	if contains(string(b), `"docs"`) || !contains(string(b), `"mine"`) {
		t.Fatalf("after the main config dropped every server: %s", b)
	}
}

func TestSyncMCPCarriesLocalScopeServers(t *testing.T) {
	home := t.TempDir()
	mainCfg := filepath.Join(home, ".claude.json")
	os.WriteFile(mainCfg, []byte(`{"projects":{"/work":{"mcpServers":{"db":{"command":"/db"}},"allowedTools":["Bash"]}}}`), 0o600)
	s := core.DefaultSettings()
	s.ClaudeHome = home
	dir := filepath.Join(t.TempDir(), "acct")
	os.MkdirAll(dir, 0o700)
	os.WriteFile(filepath.Join(dir, syncedMCPFile), []byte(`["gone"]`), 0o600)
	cfg := filepath.Join(dir, ".claude.json")
	os.WriteFile(cfg, []byte(`{"mcpServers":{"gone":{"command":"/gone"}},"projects":{"/work":{"hasTrustDialogAccepted":true}}}`), 0o600)

	if err := EnsureProfile(s, dir); err != nil {
		t.Fatal(err)
	}
	var prof struct {
		MCP      map[string]any            `json:"mcpServers"`
		Projects map[string]map[string]any `json:"projects"`
	}
	b, _ := os.ReadFile(cfg)
	json.Unmarshal(b, &prof)
	work := prof.Projects["/work"]
	if _, ok := work["mcpServers"].(map[string]any)["db"]; !ok {
		t.Fatalf("local-scope server not carried over: %s", b)
	}
	if work["hasTrustDialogAccepted"] != true || work["allowedTools"] != nil {
		t.Fatalf("project entry should keep the account's own fields only: %s", b)
	}
	if _, ok := prof.MCP["gone"]; ok {
		t.Fatalf("server listed in the old tracking file was not removed: %s", b)
	}

	os.WriteFile(mainCfg, []byte(`{"projects":{"/work":{}}}`), 0o600)
	if err := EnsureProfile(s, dir); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(cfg)
	if contains(string(b), `"db"`) {
		t.Fatalf("server removed from the main project is still in the account: %s", b)
	}
}

func TestRelayInSettings(t *testing.T) {
	home := t.TempDir()
	os.WriteFile(filepath.Join(home, "settings.json"), []byte(`{"env":{"ANTHROPIC_BASE_URL":"https://proxy.example"}}`), 0o600)
	s := core.DefaultSettings()
	s.ClaudeHome = home
	if !RelayInSettings(s) {
		t.Fatal("expected relay")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || (len(s) > 0 && (func() bool { return stringIndex(s, sub) >= 0 })()))
}

func stringIndex(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
