package grok

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nearmetips/DailiCode/internal/safety"
)

// ScanSignals classifies limit / login / ban phrases in a session's chat
// history after since. Grok does not publish five-hour / weekly windows the
// way Claude and Codex do, so the pool stays on "额度未知" until an error
// like this shows up.
func ScanSignals(sessionDir string, since time.Time) safety.Signal {
	path := filepath.Join(sessionDir, "chat_history.jsonl")
	f, err := os.Open(path)
	if err != nil {
		return safety.Signal{}
	}
	defer f.Close()
	if fi, err := f.Stat(); err == nil && fi.Size() > 2<<20 {
		_, _ = f.Seek(fi.Size()-2<<20, io.SeekStart)
	}
	var worst safety.Signal
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 16<<20)
	for sc.Scan() {
		line := sc.Bytes()
		var rec struct {
			Type      string `json:"type"`
			Role      string `json:"role"`
			Timestamp string `json:"timestamp"`
			Error     string `json:"error"`
			Message   string `json:"message"`
			Content   any    `json:"content"`
		}
		if json.Unmarshal(line, &rec) != nil {
			continue
		}
		at := time.Time{}
		if rec.Timestamp != "" {
			if t, err := time.Parse(time.RFC3339Nano, rec.Timestamp); err == nil {
				at = t
			} else if t, err := time.Parse(time.RFC3339, rec.Timestamp); err == nil {
				at = t
			}
		}
		if !since.IsZero() && !at.IsZero() && at.Before(since) {
			continue
		}
		text := rec.Error
		if text == "" {
			text = rec.Message
		}
		if text == "" && (rec.Type == "error" || rec.Role == "error" || strings.Contains(strings.ToLower(rec.Type), "error")) {
			text = contentText(rec.Content)
		}
		if text == "" {
			continue
		}
		sig := safety.Classify("", text)
		if sig.Kind == safety.Limit {
			sig.Until = safety.ResetFrom(text, at)
		}
		sig.At = at
		if sig.Kind.Worse(worst.Kind) {
			worst = sig
		}
	}
	return worst
}

func contentText(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case []any:
		var b strings.Builder
		for _, p := range t {
			m, _ := p.(map[string]any)
			if text, _ := m["text"].(string); text != "" {
				if b.Len() > 0 {
					b.WriteByte('\n')
				}
				b.WriteString(text)
			}
		}
		return strings.TrimSpace(b.String())
	default:
		return ""
	}
}
