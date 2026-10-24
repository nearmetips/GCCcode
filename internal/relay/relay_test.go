package relay

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nearmetips/DailiCode/internal/core"
)

func TestPrepareClaudeCarriesLCU(t *testing.T) {
	t.Setenv("GCCCODE_HOME", t.TempDir())
	home := t.TempDir()
	os.WriteFile(filepath.Join(home, "settings.json"), []byte(`{
  "statusLine": {"type": "command", "command": "gcccode statusline"},
  "permissions": {"deny": ["mcp__lcu__turn_ended"], "allow": ["Bash(ls:*)"]},
  "hooks": {"Stop": [
    {"hooks": [{"type": "command", "command": "echo mine"}]},
    {"hooks": [{"type": "mcp_tool", "server": "lcu", "tool": "turn_ended"}]}
  ]}
}`), 0o600)
	mainCfg := filepath.Join(home, ".claude.json")
	os.WriteFile(mainCfg, []byte(`{"mcpServers":{"lcu":{"type":"stdio","command":"/lcu/node"},"docs":{"command":"/docs"}}}`), 0o600)
	s := core.DefaultSettings()
	s.ClaudeHome = home
	s.Relay.AnthropicURL = "https://relay.example"
	s.Relay.Token = "sk-relay"

	dir, err := PrepareClaude(s)
	if err != nil {
		t.Fatal(err)
	}
	set, _ := os.ReadFile(filepath.Join(dir, "settings.json"))
	text := string(set)
	for _, want := range []string{"https://relay.example", "turn_ended", "mcp__lcu__turn_ended"} {
		if !strings.Contains(text, want) {
			t.Fatalf("relay settings missing %s: %s", want, text)
		}
	}
	for _, leaked := range []string{"echo mine", "Bash(ls:*)", "statusline"} {
		if strings.Contains(text, leaked) {
			t.Fatalf("relay settings picked up %s: %s", leaked, text)
		}
	}
	cfg, _ := os.ReadFile(filepath.Join(dir, ".claude.json"))
	if !strings.Contains(string(cfg), "/lcu/node") || strings.Contains(string(cfg), "/docs") {
		t.Fatalf("relay config: %s", cfg)
	}

	os.WriteFile(mainCfg, []byte(`{"mcpServers":{}}`), 0o600)
	if _, err := PrepareClaude(s); err != nil {
		t.Fatal(err)
	}
	cfg, _ = os.ReadFile(filepath.Join(dir, ".claude.json"))
	if strings.Contains(string(cfg), "/lcu/node") {
		t.Fatalf("lcu removed from the main config stayed in the relay dir: %s", cfg)
	}
}

func TestCodexRelayRoundTrip(t *testing.T) {
	t.Setenv("GCCCODE_HOME", t.TempDir())
	home := t.TempDir()
	orig := "model = \"gpt\"\nmodel_provider = \"openai\"\n\n[features]\nenabled = true\n"
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}
	s := core.DefaultSettings()
	s.CodexHome = home
	s.Relay.OpenAIURL = "https://api.example/v1"
	s.Relay.Token = "sk-test"
	restore, err := ActivateCodex(s)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(home, "config.toml"))
	text := string(got)
	if !strings.Contains(text, `model_provider = "gcccode"`) || strings.Contains(text, "sk-test") {
		t.Fatalf("provider missing or token written into the file: %s", text)
	}
	if strings.Contains(text, "model_provider = \"openai\"\n") && !strings.Contains(text, "# gcccode-saved") {
		t.Fatal("original provider was not saved")
	}
	if err := restore(); err != nil {
		t.Fatal(err)
	}
	back, _ := os.ReadFile(filepath.Join(home, "config.toml"))
	if string(back) != orig {
		t.Fatalf("not restored:\n%s", back)
	}
}
