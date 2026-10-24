package grok

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nearmetips/DailiCode/internal/core"
	"github.com/nearmetips/DailiCode/internal/safety"
)

func TestRecentSkipsSubagentsAndFiltersCwd(t *testing.T) {
	home, work, other := t.TempDir(), t.TempDir(), t.TempDir()
	s := core.DefaultSettings()
	s.GrokHome = home
	now := time.Now()
	writeSession := func(id, cwd, summary, agent string, mod time.Time) {
		group := filepath.Join(home, "sessions", "%2Fwork")
		dir := filepath.Join(group, id)
		os.MkdirAll(dir, 0o700)
		body := `{"info":{"id":"` + id + `","cwd":"` + cwd + `"},"session_summary":"` + summary + `","updated_at":"` + mod.Format(time.RFC3339Nano) + `"`
		if agent != "" {
			body += `,"agent_id":"` + agent + `"`
		}
		body += "}"
		os.WriteFile(filepath.Join(dir, "summary.json"), []byte(body), 0o600)
	}
	writeSession("main", work, "修登录", "", now.Add(-time.Minute))
	writeSession("sub", work, "子代理", "agent-1", now)
	writeSession("elsewhere", other, "别的目录", "", now.Add(-2*time.Minute))

	got := Recent(s, "", work, 0)
	if len(got) != 1 || got[0].ID != "main" || got[0].Summary != "修登录" {
		t.Fatalf("got %+v", got)
	}
	if sess, ok := FindSession(s, "", "main"); !ok || sess.Cwd != work {
		t.Fatalf("find %+v %v", sess, ok)
	}
	if _, ok := FindSession(s, "", "sub"); ok {
		t.Fatal("subagent session should be hidden")
	}
}

func TestScanSignalsReadsLimitErrors(t *testing.T) {
	dir := t.TempDir()
	since := time.Now().Add(-time.Hour)
	os.WriteFile(filepath.Join(dir, "chat_history.jsonl"), []byte(
		`{"type":"error","timestamp":"`+time.Now().Format(time.RFC3339)+`","error":"You've hit your usage limit. Try again in 2 hours."}`+"\n",
	), 0o600)
	sig := ScanSignals(dir, since)
	if sig.Kind != safety.Limit {
		t.Fatalf("kind=%v reason=%q", sig.Kind, sig.Reason)
	}
}
