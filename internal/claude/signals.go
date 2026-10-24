package claude

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"strings"
	"time"

	"github.com/nearmetips/DailiCode/internal/core"
	"github.com/nearmetips/DailiCode/internal/safety"
)

// StatusInput is the part of Claude Code's statusline JSON GCC Code reads.
type StatusInput struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	Cwd            string `json:"cwd"`
	Model          struct {
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
	} `json:"model"`
	RateLimits *struct {
		FiveHour *limitWindow `json:"five_hour"`
		SevenDay *limitWindow `json:"seven_day"`
	} `json:"rate_limits"`
}

type limitWindow struct {
	UsedPercentage float64 `json:"used_percentage"`
	ResetsAt       int64   `json:"resets_at"`
}

// ParseStatusInput decodes the statusline JSON; unknown fields are ignored.
func ParseStatusInput(r io.Reader) (StatusInput, error) {
	var in StatusInput
	b, err := io.ReadAll(io.LimitReader(r, 4<<20))
	if err != nil {
		return in, err
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return in, nil
	}
	return in, json.Unmarshal(b, &in)
}

// QuotaFrom turns the statusline rate limits into a quota snapshot. ok is
// false when Claude sent none (an API-key session, or before the first reply).
func QuotaFrom(id string, in StatusInput, now time.Time) (core.Quota, bool) {
	if in.RateLimits == nil || (in.RateLimits.FiveHour == nil && in.RateLimits.SevenDay == nil) {
		return core.Quota{}, false
	}
	q := core.Quota{AccountID: id, Source: "claude-statusline", UpdatedAt: now}
	if w := in.RateLimits.FiveHour; w != nil {
		q.FiveHour = &core.Window{UsedPct: w.UsedPercentage, ResetAt: w.ResetsAt}
	}
	if w := in.RateLimits.SevenDay; w != nil {
		q.Weekly = &core.Window{UsedPct: w.UsedPercentage, ResetAt: w.ResetsAt}
	}
	return q, true
}

// ScanTranscript classifies the API errors Claude Code recorded in a session
// transcript after since. Only lines Claude itself marks as API errors are
// read, so a conversation that merely mentions a ban is never mistaken for one.
func ScanTranscript(path string, since time.Time) safety.Signal {
	f, err := os.Open(path)
	if err != nil {
		return safety.Signal{}
	}
	defer f.Close()
	if fi, err := f.Stat(); err == nil && fi.Size() > 2<<20 {
		f.Seek(fi.Size()-2<<20, io.SeekStart)
	}
	var worst safety.Signal
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 16<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if !strings.Contains(string(line), `"isApiErrorMessage":true`) {
			continue
		}
		var rec struct {
			Timestamp time.Time `json:"timestamp"`
			Error     string    `json:"error"`
			Message   struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &rec) != nil {
			continue
		}
		if !since.IsZero() && rec.Timestamp.Before(since) {
			continue
		}
		var text strings.Builder
		for _, c := range rec.Message.Content {
			text.WriteString(c.Text)
			text.WriteString(" ")
		}
		sig := safety.Classify(rec.Error, text.String())
		sig.At = rec.Timestamp
		if sig.Kind == safety.Limit {
			sig.Until = safety.ResetFrom(text.String(), rec.Timestamp)
		}
		if sig.Kind.Worse(worst.Kind) {
			worst = sig
		}
	}
	return worst
}
