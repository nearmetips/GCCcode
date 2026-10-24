package agy

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/nearmetips/DailiCode/internal/safety"
)

// glogLine is agy's log format: level, MMDD, time, thread, file:line, message.
var glogLine = regexp.MustCompile(`([IWEF])(\d{4}) (\d{2}:\d{2}:\d{2}\.\d+)\s+\d+\s+[^\]\s]+\]\s?(.*)$`)

// agy's logs mention quota on every refresh, so only these phrases on
// warning or error lines count. Model capacity and image generation limits
// are not the account's quota.
var (
	limitWords = []string{
		"resource_exhausted", "resource has been exhausted", "quota reached", "quota exceeded",
		"quota has been exceeded", "quota is used up", "out of credits", "exhausted your",
	}
	notLimitWords = []string{"capacity", "overloaded", "image generation"}
	loginWords    = []string{"invalid_grant", "token has been expired or revoked"}
)

// ScanSignals classifies limit / login / ban errors in the logs agy wrote
// in home after since. agy publishes no usage windows, so the pool stays on
// "额度未知" until one of these shows up.
func ScanSignals(home string, since time.Time) safety.Signal {
	dir := filepath.Join(cliDir(home), "log")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return safety.Signal{}
	}
	var worst safety.Signal
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".log") {
			continue
		}
		if fi, err := e.Info(); err != nil || fi.ModTime().Before(since.Add(-time.Minute)) {
			continue
		}
		if sig := scanLog(filepath.Join(dir, e.Name()), since); sig.Kind.Worse(worst.Kind) {
			worst = sig
		}
	}
	return worst
}

func scanLog(path string, since time.Time) safety.Signal {
	f, err := os.Open(path)
	if err != nil {
		return safety.Signal{}
	}
	defer f.Close()
	if fi, err := f.Stat(); err == nil && fi.Size() > 2<<20 {
		_, _ = f.Seek(fi.Size()-2<<20, io.SeekStart)
	}
	now := time.Now()
	var worst safety.Signal
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 16<<20)
	for sc.Scan() {
		m := glogLine.FindStringSubmatch(sc.Text())
		if m == nil || m[1] == "I" {
			continue
		}
		at := logTime(m[2], m[3], now)
		if !since.IsZero() && !at.IsZero() && at.Before(since) {
			continue
		}
		if sig := classify(m[4], at); sig.Kind.Worse(worst.Kind) {
			worst = sig
		}
	}
	return worst
}

// logTime reads glog's year-less timestamp as local time in the last year.
func logTime(mmdd, clock string, now time.Time) time.Time {
	t, err := time.ParseInLocation("2006 0102 15:04:05.999999", now.Format("2006")+" "+mmdd+" "+clock, time.Local)
	if err != nil {
		return time.Time{}
	}
	if t.After(now.Add(24 * time.Hour)) {
		t = t.AddDate(-1, 0, 0)
	}
	return t
}

func classify(msg string, at time.Time) safety.Signal {
	if sig := safety.Classify("", msg); sig.Kind == safety.Banned {
		sig.At = at
		return sig
	}
	low := strings.ToLower(msg)
	for _, w := range loginWords {
		if strings.Contains(low, w) {
			return safety.Signal{Kind: safety.NeedsLogin, Reason: oneLine(msg, 160), At: at}
		}
	}
	for _, w := range notLimitWords {
		if strings.Contains(low, w) {
			return safety.Signal{}
		}
	}
	for _, w := range limitWords {
		if strings.Contains(low, w) {
			return safety.Signal{Kind: safety.Limit, Reason: oneLine(msg, 160), Until: safety.ResetFrom(msg, at), At: at}
		}
	}
	return safety.Signal{}
}
