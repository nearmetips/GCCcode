package claude

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nearmetips/DailiCode/internal/core"
)

func writeTranscript(t *testing.T, path, cwd string, mod time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"type":"permission-mode","permissionMode":"default"}` + "\n" +
		`{"type":"user","cwd":"` + cwd + `","message":{"role":"user","content":"hi"}}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mod, mod); err != nil {
		t.Fatal(err)
	}
}

func TestLatestStaysInTheLaunchDirectory(t *testing.T) {
	home := t.TempDir()
	s := core.DefaultSettings()
	s.ClaudeHome = home
	work, other := t.TempDir(), t.TempDir()
	now := time.Now()
	projects := filepath.Join(home, "projects")
	writeTranscript(t, filepath.Join(projects, "work", "mine.jsonl"), work, now.Add(-2*time.Minute))
	writeTranscript(t, filepath.Join(projects, "other", "parallel.jsonl"), other, now)
	writeTranscript(t, filepath.Join(projects, "work", "mine", "subagents", "agent-1.jsonl"), work, now.Add(time.Second))

	got, ok := Latest(s, work, now.Add(-time.Hour), nil)
	if !ok || got.ID != "mine" {
		t.Fatalf("got %+v %v, want the session started in the launch directory", got, ok)
	}
	if cwd := SessionCwd(got.Path); cwd != work {
		t.Fatalf("cwd = %q", cwd)
	}

	writeTranscript(t, filepath.Join(projects, "work", "theirs.jsonl"), work, now.Add(-time.Minute))
	got, ok = Latest(s, work, now.Add(-time.Hour), func(id string) bool { return id == "theirs" })
	if !ok || got.ID != "mine" {
		t.Fatalf("got %+v %v, want the session no other account owns", got, ok)
	}
	if _, ok := Latest(s, work, now, nil); ok {
		t.Fatal("a session older than since was returned")
	}
}
