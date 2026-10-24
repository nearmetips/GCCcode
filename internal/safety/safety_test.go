package safety

import (
	"testing"
	"time"
)

func TestResetFrom(t *testing.T) {
	local := time.FixedZone("UTC+8", 8*3600)
	prev := time.Local
	time.Local = local
	defer func() { time.Local = prev }()
	at := time.Date(2026, 10, 6, 14, 39, 0, 0, time.UTC) // 22:39 local, as a transcript stamps it
	day := func(d, h, m int) int64 { return time.Date(2026, 10, d, h, m, 0, 0, local).Unix() }
	cases := []struct {
		text string
		want int64
	}{
		{"You've hit your limit · resets 11pm", day(6, 23, 0)},
		{"5-hour limit reached ∙ resets 3am", day(7, 3, 0)},
		{"limit reached · resets 3:30pm", day(7, 15, 30)},
		{"Claude usage limit reached. Your limit will reset at 5am (UTC).", time.Date(2026, 10, 7, 5, 0, 0, 0, time.UTC).Unix()},
		{"Weekly limit reached · resets Oct 9, 5am", day(9, 5, 0)},
		{"resets Oct 9 at 12pm", day(9, 12, 0)},
		{"resets 12am", day(7, 0, 0)},
		{"You've hit your usage limit. Try again at 8:52 PM.", day(7, 20, 52)},
		{"You've hit your usage limit. Upgrade to Pro or try again in 2 hours 13 minutes.", at.Add(2*time.Hour + 13*time.Minute).Unix()},
		{"try again in 4 days 21 hours 32 minutes", at.Add(4*24*time.Hour + 21*time.Hour + 32*time.Minute).Unix()},
		{"usage limit reached", 0},
		{"resets 13pm", 0},
	}
	for _, c := range cases {
		if got := ResetFrom(c.text, at); got != c.want {
			t.Errorf("%q: got %s want %s", c.text, time.Unix(got, 0).In(local), time.Unix(c.want, 0).In(local))
		}
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		code, text string
		kind       Kind
	}{
		{"", "Your organization has been disabled", Banned},
		{"authentication_failed", "Please run /login", NeedsLogin},
		{"", "OAuth token has been revoked", NeedsLogin},
		{"rate_limit", "usage limit reached", Limit},
		{"", "Server is temporarily limiting requests (not your usage limit)", None},
		{"", "just chatting about a banned book", None},
	}
	for _, tc := range cases {
		got := Classify(tc.code, tc.text)
		if got.Kind != tc.kind {
			t.Errorf("%s %q -> %s, want %s", tc.code, tc.text, got.Kind, tc.kind)
		}
	}
}
