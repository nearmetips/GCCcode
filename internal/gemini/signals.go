package gemini

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"strings"
	"time"

	"github.com/nearmetips/DailiCode/internal/safety"
)

// ScanSignals classifies limit / login / ban phrases in a Gemini chat file
// after since. When Gemini never surfaces windows, the pool keeps selecting
// with "额度未知".
func ScanSignals(chatPath string, since time.Time) safety.Signal {
	f, err := os.Open(chatPath)
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
		trim := strings.TrimSpace(string(line))
		if trim == "" {
			continue
		}
		// Whole-file JSON chats: classify once on the blob.
		if strings.HasPrefix(trim, "{") && strings.Contains(trim, `"messages"`) {
			sig := safety.Classify("", trim)
			if sig.Kind == safety.Limit {
				sig.Until = safety.ResetFrom(trim, time.Now())
			}
			if sig.Kind.Worse(worst.Kind) {
				worst = sig
			}
			continue
		}
		var rec struct {
			Type      string `json:"type"`
			Role      string `json:"role"`
			Error     string `json:"error"`
			Message   string `json:"message"`
			Timestamp string `json:"timestamp"`
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
		if text == "" && (rec.Type == "error" || rec.Role == "error") {
			text = contentString(rec.Content)
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
