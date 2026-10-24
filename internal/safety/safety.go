// Package safety classifies the errors official clients report into the
// signals GCC Code acts on: a spent quota, a lost login, or a risk-control hit.
package safety

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Kind int

const (
	None Kind = iota
	Limit
	NeedsLogin
	Banned
)

func (k Kind) String() string {
	switch k {
	case Limit:
		return "额度用完"
	case NeedsLogin:
		return "需要重新登录"
	case Banned:
		return "疑似风控/封禁"
	}
	return "正常"
}

// Worse reports whether k is more severe than other.
func (k Kind) Worse(other Kind) bool { return k > other }

type Signal struct {
	Kind   Kind
	Reason string
	At     time.Time
	// Until is when a spent quota comes back, as the message said; 0 when
	// it did not say.
	Until int64
}

var (
	resetClock = regexp.MustCompile(`(?i)\b(?:resets?|try again)(?:\s+(?:at|on))?\s+(?:([a-z]{3,9})\.?\s+(\d{1,2}),?\s+(?:at\s+)?)?(\d{1,2})(?::(\d{2}))?\s*([ap])\.?m\.?(?:\s*\(([^)]+)\))?`)
	resetAfter = regexp.MustCompile(`(?i)\btry again in\s+((?:\d+\s*(?:days?|d|hours?|hrs?|h|minutes?|mins?|m)\b[\s,]*(?:and\s+)?)+)`)
	resetPart  = regexp.MustCompile(`(?i)(\d+)\s*(days?|d|hours?|hrs?|h|minutes?|mins?|m)\b`)
)

// ResetFrom reads when a limit message says the quota comes back, such as
// "resets 11pm", "try again at 3:30 PM", "resets Oct 9, 5am (Asia/Shanghai)"
// or "try again in 2 hours 13 minutes". Clock times are the next such time
// after at, the moment the message was written, in the named zone or else
// the machine's: clients print them in local time, while transcripts stamp
// at in UTC. It returns 0 when the text names no time.
func ResetFrom(text string, at time.Time) int64 {
	if m := resetClock.FindStringSubmatch(text); m != nil {
		loc := time.Local
		if m[6] != "" {
			if l, err := time.LoadLocation(strings.TrimSpace(m[6])); err == nil {
				loc = l
			}
		}
		hour, _ := strconv.Atoi(m[3])
		min, _ := strconv.Atoi(m[4])
		if hour < 1 || hour > 12 || min > 59 {
			return 0
		}
		hour %= 12
		if strings.EqualFold(m[5], "p") {
			hour += 12
		}
		ref := at.In(loc)
		if m[1] != "" {
			month, ok := monthOf(m[1])
			day, _ := strconv.Atoi(m[2])
			if !ok || day < 1 || day > 31 {
				return 0
			}
			t := time.Date(ref.Year(), month, day, hour, min, 0, 0, loc)
			if t.Before(ref.Add(-24 * time.Hour)) {
				t = t.AddDate(1, 0, 0)
			}
			return t.Unix()
		}
		t := time.Date(ref.Year(), ref.Month(), ref.Day(), hour, min, 0, 0, loc)
		if !t.After(ref) {
			t = t.AddDate(0, 0, 1)
		}
		return t.Unix()
	}
	if m := resetAfter.FindStringSubmatch(text); m != nil {
		var d time.Duration
		for _, p := range resetPart.FindAllStringSubmatch(m[1], -1) {
			n, _ := strconv.Atoi(p[1])
			switch u := strings.ToLower(p[2]); {
			case strings.HasPrefix(u, "d"):
				d += time.Duration(n) * 24 * time.Hour
			case strings.HasPrefix(u, "h"):
				d += time.Duration(n) * time.Hour
			default:
				d += time.Duration(n) * time.Minute
			}
		}
		if d > 0 {
			return at.Add(d).Unix()
		}
	}
	return 0
}

func monthOf(s string) (time.Month, bool) {
	s = strings.ToLower(s)
	if len(s) < 3 {
		return 0, false
	}
	for m := time.January; m <= time.December; m++ {
		if strings.HasPrefix(strings.ToLower(m.String()), s[:3]) {
			return m, true
		}
	}
	return 0, false
}

var bannedPhrases = []string{
	"organization has been disabled",
	"account has been disabled",
	"account has been suspended",
	"account is suspended",
	"has been banned",
	"usage allocation has been disabled",
	"account_deactivated",
	"has been deactivated",
}

var loginPhrases = []string{
	"oauth token has been revoked",
	"oauth token has expired",
	"token has expired",
	"token_expired",
	"refresh_token_reused",
	"refresh token",
	"invalid api key",
	"please run /login",
	"please log in",
	"not logged in",
	"unauthorized",
	"401",
}

var limitPhrases = []string{
	"usage limit",
	"hit your limit",
	"limit reached",
	"rate_limit_reached",
	"usage_limit_reached",
	"rate limit",
	"quota",
	"/upgrade",
	"try again at",
	"resets at",
}

// notQuota are 429s that are the vendor's capacity, not the account's quota.
var notQuota = []string{
	"not your usage limit",
	"context limit",
	"overloaded",
	"temporarily limiting requests",
}

// Classify maps an error code and its message to a signal.
func Classify(code, text string) Signal {
	c := strings.ToLower(code)
	t := strings.ToLower(text)
	for _, p := range bannedPhrases {
		if strings.Contains(t, p) || strings.Contains(c, p) {
			return Signal{Kind: Banned, Reason: snippet(text, code)}
		}
	}
	if c == "authentication_failed" || c == "authentication_error" || c == "permission_error" {
		return Signal{Kind: NeedsLogin, Reason: snippet(text, code)}
	}
	for _, p := range loginPhrases {
		if strings.Contains(t, p) {
			return Signal{Kind: NeedsLogin, Reason: snippet(text, code)}
		}
	}
	for _, p := range notQuota {
		if strings.Contains(t, p) {
			return Signal{}
		}
	}
	if c == "rate_limit" || c == "billing_error" || c == "usage_limit_reached" {
		return Signal{Kind: Limit, Reason: snippet(text, code)}
	}
	for _, p := range limitPhrases {
		if strings.Contains(t, p) {
			return Signal{Kind: Limit, Reason: snippet(text, code)}
		}
	}
	return Signal{}
}

func snippet(text, code string) string {
	s := strings.TrimSpace(text)
	if s == "" {
		s = code
	}
	if r := []rune(s); len(r) > 160 {
		s = string(r[:160]) + "…"
	}
	return s
}
