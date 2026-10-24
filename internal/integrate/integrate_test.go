package integrate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nearmetips/DailiCode/internal/core"
)

func setup(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("GCCCODE_HOME", root)
	home := filepath.Join(root, "claude-home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{"claude_home": home})
	if err := os.WriteFile(filepath.Join(root, "settings.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	return home
}

func readSettings(t *testing.T, home string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(home, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]any{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestStatusLineKeepsAndRestoresTheUsersOwn(t *testing.T) {
	home := setup(t)
	mine := map[string]any{"type": "command", "command": "echo mine", "padding": 0.0}
	b, _ := json.Marshal(map[string]any{"theme": "dark", "statusLine": mine})
	os.WriteFile(filepath.Join(home, "settings.json"), b, 0o600)

	for i := 0; i < 2; i++ {
		prev, err := InstallStatusLine()
		if err != nil || prev != "echo mine" {
			t.Fatalf("install #%d: prev=%q err=%v", i+1, prev, err)
		}
	}
	line, _ := readSettings(t, home)["statusLine"].(map[string]any)
	if !IsOurs(line) || line["padding"] != 0.0 {
		t.Fatalf("statusLine = %v", line)
	}
	st, err := core.Load()
	if err != nil {
		t.Fatal(err)
	}
	out := RunPrevious(st.Settings, []byte(`{}`))
	if out == nil {
		t.Fatal("the user's status line is not run")
	}
	if got := <-out; got != "mine" {
		t.Fatalf("previous output = %q", got)
	}

	removed, err := RemoveStatusLine()
	if err != nil || !removed {
		t.Fatalf("remove: %v %v", removed, err)
	}
	m := readSettings(t, home)
	if !reflect.DeepEqual(m["statusLine"], mine) || m["theme"] != "dark" {
		t.Fatalf("not restored: %v", m)
	}
	if st, _ := core.Load(); st.Settings.StatusLinePrev != nil {
		t.Fatalf("saved status line left behind: %v", st.Settings.StatusLinePrev)
	}
}

func TestStatusLineWithoutAPreviousOne(t *testing.T) {
	home := setup(t)
	if prev, err := InstallStatusLine(); err != nil || prev != "" {
		t.Fatalf("prev=%q err=%v", prev, err)
	}
	st, _ := core.Load()
	if RunPrevious(st.Settings, nil) != nil {
		t.Fatal("ran a previous status line that never existed")
	}
	if removed, err := RemoveStatusLine(); err != nil || !removed {
		t.Fatalf("remove: %v %v", removed, err)
	}
	if _, ok := readSettings(t, home)["statusLine"]; ok {
		t.Fatal("statusLine left in settings.json")
	}
	if removed, _ := RemoveStatusLine(); removed {
		t.Fatal("removed twice")
	}
}

func TestShellQuoting(t *testing.T) {
	if got := shellQuote("/usr/local/bin/gcccode"); got != "/usr/local/bin/gcccode" {
		t.Fatalf("plain path quoted: %s", got)
	}
	if got := shellQuote("/Users/a b/it's/gcccode"); got != `'/Users/a b/it'\''s/gcccode'` {
		t.Fatalf("got %s", got)
	}
	if !strings.HasPrefix(fishQuote("/a b/gcccode"), "'") {
		t.Fatal("fish path with a space not quoted")
	}
}

func TestShellInitWrapsEveryClient(t *testing.T) {
	sh := ShellInit("bash")
	for _, name := range []string{"claude", "codex", "gemini", "grok", "agy"} {
		if !strings.Contains(sh, name+"() {") {
			t.Fatalf("bash missing %s wrapper:\n%s", name, sh)
		}
	}
	fish := ShellInit("fish")
	for _, name := range []string{"claude", "codex", "gemini", "grok", "agy"} {
		if !strings.Contains(fish, "function "+name) {
			t.Fatalf("fish missing %s wrapper:\n%s", name, fish)
		}
	}
}
