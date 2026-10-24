package lcu

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nearmetips/DailiCode/internal/core"
)

// lcuSettings mirrors what lcu setup --agent claude-code --approval auto
// writes into settings.json, next to a user's own hook and rules.
const lcuSettings = `{
  "theme": "dark",
  "permissions": {
    "allow": ["Bash(ls:*)", "mcp__lcu__js", "mcp__lcu__js_reset", "mcp__lcuish__x"],
    "deny": ["mcp__lcu__turn_ended", "mcp__lcu__js_add_node_module_dir", "mcp__lcu__set_turn_context"]
  },
  "hooks": {
    "PreToolUse": [
      {"matcher": "Bash", "hooks": [{"type": "command", "command": "echo mine"}]},
      {"matcher": "mcp__lcu__js|mcp__lcu__js_reset", "hooks": [{"type": "mcp_tool", "server": "lcu", "tool": "set_turn_context", "input": {"session_id": "${session_id}"}}]}
    ],
    "Stop": [
      {"hooks": [{"type": "mcp_tool", "server": "lcu", "tool": "turn_ended", "input": {"hook_event_name": "Stop"}}]}
    ]
  }
}`

func TestCommandFromPrefix(t *testing.T) {
	prefix := t.TempDir()
	bin := filepath.Join(prefix, "current", "bin", "lcu")
	os.MkdirAll(filepath.Dir(bin), 0o755)
	os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755)
	os.WriteFile(filepath.Join(prefix, "current", "bundle.json"), []byte(`{"architecture":"arm64","files":{},"version":"0.9.5"}`), 0o644)
	t.Setenv("LCU_PREFIX", prefix)
	got, ok := Command()
	if !ok || got != bin {
		t.Fatalf("Command() = %q %v", got, ok)
	}
	if v := Version(got); v != "0.9.5" {
		t.Fatalf("Version = %q", v)
	}
}

func TestEnvDropsConfigDirAndPinsCodexHome(t *testing.T) {
	s := core.DefaultSettings()
	base := []string{"PATH=/bin", "CLAUDE_CONFIG_DIR=/pool/claude-1", "CODEX_HOME=/elsewhere"}
	if got := Env(base, s); slices.Contains(got, "CLAUDE_CONFIG_DIR=/pool/claude-1") || !slices.Contains(got, "CODEX_HOME=/elsewhere") {
		t.Fatalf("default settings: %v", got)
	}
	s.CodexHome = "/pool/codex"
	got := Env(base, s)
	if !slices.Contains(got, "CODEX_HOME=/pool/codex") || slices.Contains(got, "CODEX_HOME=/elsewhere") || !slices.Contains(got, "PATH=/bin") {
		t.Fatalf("pinned codex home: %v", got)
	}
}

func TestSetupArgs(t *testing.T) {
	agents := []string{"claude-code", "codex"}
	got := SetupArgs([]string{"--chrome"}, agents)
	want := []string{"--agent", "claude-code", "--agent", "codex", "--chrome"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v", got)
	}
	for _, args := range [][]string{{"--agent", "pi"}, {"--agent=pi"}, {"--reconcile"}} {
		if got := SetupArgs(args, agents); !slices.Equal(got, args) {
			t.Fatalf("%v rewritten to %v", args, got)
		}
	}
}

func TestCopyClaudeSettingsTakesOnlyLCU(t *testing.T) {
	var src map[string]any
	if err := json.Unmarshal([]byte(lcuSettings), &src); err != nil {
		t.Fatal(err)
	}
	dst := map[string]any{"env": map[string]any{"ANTHROPIC_BASE_URL": "https://relay.example"}}
	CopyClaudeSettings(dst, src)
	b, _ := json.Marshal(dst)
	text := string(b)
	for _, want := range []string{`"set_turn_context"`, `"turn_ended"`, `"mcp__lcu__js"`, `"mcp__lcu__js_add_node_module_dir"`, "relay.example"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %s: %s", want, text)
		}
	}
	for _, leaked := range []string{"echo mine", "Bash(ls:*)", "mcp__lcuish__x", "dark"} {
		if strings.Contains(text, leaked) {
			t.Fatalf("%s leaked: %s", leaked, text)
		}
	}
}

func TestCopyClaudeServer(t *testing.T) {
	entry := map[string]any{"type": "stdio", "command": "/x/node", "args": []any{"/x/claude.mjs"}}
	src := map[string]any{"mcpServers": map[string]any{"lcu": entry, "other": map[string]any{}}}
	dst := map[string]any{"numStartups": 3.0}
	if !CopyClaudeServer(dst, src) || CopyClaudeServer(dst, src) {
		t.Fatal("first copy should change dst, the second should not")
	}
	servers := dst["mcpServers"].(map[string]any)
	if _, ok := servers["other"]; ok {
		t.Fatal("only the lcu server is carried")
	}
	if !CopyClaudeServer(dst, map[string]any{}) {
		t.Fatal("removal not reported")
	}
	if _, ok := servers["lcu"]; ok || dst["numStartups"] != 3.0 {
		t.Fatalf("after removal: %v", dst)
	}
}

func TestInspect(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, ".claude.json")
	set := filepath.Join(dir, "settings.json")
	if got := InspectClaude(cfg, set); got.Server || got.Hooks {
		t.Fatalf("empty dir: %+v", got)
	}
	os.WriteFile(cfg, []byte(`{"mcpServers":{"lcu":{"command":"x"}}}`), 0o600)
	os.WriteFile(set, []byte(lcuSettings), 0o600)
	if got := InspectClaude(cfg, set); !got.Server || !got.Hooks {
		t.Fatalf("registered dir: %+v", got)
	}
	if CodexRegistered(dir) {
		t.Fatal("no config.toml yet")
	}
	os.WriteFile(filepath.Join(dir, "config.toml"), []byte("model = \"gpt\"\n\n[mcp_servers.lcuish]\ncommand = \"y\"\n"), 0o600)
	if CodexRegistered(dir) {
		t.Fatal("a server with a longer name is not lcu")
	}
	os.WriteFile(filepath.Join(dir, "config.toml"), []byte("[mcp_servers.lcu]\ncommand = \"x\"\n\n[mcp_servers.lcu.tools.js]\napproval_mode = \"approve\"\n"), 0o600)
	if !CodexRegistered(dir) {
		t.Fatal("lcu table not found")
	}
}

func TestFetchVerifiesChecksum(t *testing.T) {
	target, err := Target()
	if err != nil {
		t.Skip(err)
	}
	name := "lcu-0.9.5-" + target + ".tar.gz"
	archive := releaseArchive(t, strings.TrimSuffix(name, ".tar.gz"))
	sum := sha256.Sum256(archive)
	good := hex.EncodeToString(sum[:]) + "  " + name + "\n"
	sidecar := good
	mux := http.NewServeMux()
	mux.HandleFunc("/amontlabs/lcu/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/amontlabs/lcu/releases/tag/v0.9.5", http.StatusFound)
	})
	mux.HandleFunc("/amontlabs/lcu/releases/download/v0.9.5/"+name, func(w http.ResponseWriter, r *http.Request) {
		w.Write(archive)
	})
	mux.HandleFunc("/amontlabs/lcu/releases/download/v0.9.5/"+name+".sha256", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(sidecar))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	old := githubURL
	githubURL = srv.URL
	defer func() { githubURL = old }()

	parent := t.TempDir()
	r, err := Fetch(Download{}, parent)
	if err != nil {
		t.Fatal(err)
	}
	if r.Version != "0.9.5" || !fileExists(filepath.Join(r.Dir, "scripts", "install.sh")) || fileExists(filepath.Join(r.Work, name)) {
		t.Fatalf("release = %+v", r)
	}
	os.RemoveAll(r.Work)

	sidecar = strings.Repeat("0", 64) + "  " + name + "\n"
	if _, err := Fetch(Download{Version: "0.9.5"}, parent); err == nil || !strings.Contains(err.Error(), "校验失败") {
		t.Fatalf("tampered archive accepted: %v", err)
	}
	if left, _ := os.ReadDir(parent); len(left) != 0 {
		t.Fatalf("failed fetch left %v behind", left)
	}
	if _, err := Fetch(Download{Version: "../../x"}, parent); err == nil {
		t.Fatal("a version with a path in it was accepted")
	}
}

func releaseArchive(t *testing.T, root string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, f := range []struct {
		name string
		mode int64
		body string
	}{
		{root + "/bundle.json", 0o644, `{"version":"0.9.5"}`},
		{root + "/scripts/install.sh", 0o755, "#!/bin/sh\nexit 0\n"},
	} {
		if err := tw.WriteHeader(&tar.Header{Name: f.name, Mode: f.mode, Size: int64(len(f.body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		tw.Write([]byte(f.body))
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}
