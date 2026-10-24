package gemini

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nearmetips/DailiCode/internal/core"
	"github.com/nearmetips/DailiCode/internal/safety"
)

func TestRecentReadsChatFiles(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	s := core.DefaultSettings()
	s.GeminiHome = home
	now := time.Now()
	chat := filepath.Join(home, "tmp", "hash", "chats", "sess-1.json")
	os.MkdirAll(filepath.Dir(chat), 0o755)
	body := `{"sessionId":"sess-1","cwd":"` + work + `","messages":[{"role":"user","content":"修 Gemini 登录"}]}`
	os.WriteFile(chat, []byte(body), 0o600)
	os.Chtimes(chat, now, now)

	got := Recent(s, "", work, 0)
	if len(got) != 1 || got[0].ID != "sess-1" || got[0].Title != "修 Gemini 登录" {
		t.Fatalf("got %+v", got)
	}
	if sess, ok := FindSession(s, "", "sess-1"); !ok || sess.Path != chat {
		t.Fatalf("find %+v %v", sess, ok)
	}
}

func TestScanSignalsReadsLimitErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chat.jsonl")
	os.WriteFile(path, []byte(
		`{"type":"error","timestamp":"`+time.Now().Format(time.RFC3339)+`","error":"rate limit exceeded, try again in 1 hour"}`+"\n",
	), 0o600)
	sig := ScanSignals(path, time.Now().Add(-time.Hour))
	if sig.Kind != safety.Limit {
		t.Fatalf("kind=%v reason=%q", sig.Kind, sig.Reason)
	}
}
