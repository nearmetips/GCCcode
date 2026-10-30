package claude

import (
	"strings"
	"testing"
	"time"

	"github.com/nearmetips/DailiCode/internal/core"
)

const sampleToken = "sk-ant-oat01-AbCdEfGhIjKlMnOpQrStUvWxYz0123456789_-AbCdEfGhIjKlMnOpQrStUvWxYz-AA"

func TestCleanTokenFindsThePastedToken(t *testing.T) {
	wrapped := "│ " + sampleToken[:40] + "\n│ " + sampleToken[40:] + " │\n"
	for name, pasted := range map[string]string{
		"bare":        sampleToken + "\n",
		"export line": "export CLAUDE_CODE_OAUTH_TOKEN=\"" + sampleToken + "\"",
		"wrapped":     wrapped,
	} {
		if got := CleanToken(pasted); got != sampleToken {
			t.Errorf("%s: got %q", name, got)
		}
	}
}

func TestTokenProblemRejectsWhatIsNotASetupToken(t *testing.T) {
	if msg := TokenProblem(sampleToken); msg != "" {
		t.Fatalf("rejected a setup-token: %s", msg)
	}
	for _, bad := range []string{"", "sk-ant-api03-" + strings.Repeat("x", 60), "sk-ant-oat01-short", "hello"} {
		if TokenProblem(bad) == "" {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestTokenAccountIsReadyUntilItsTokenExpires(t *testing.T) {
	t.Setenv("GCCCODE_HOME", t.TempDir())
	s := core.DefaultSettings()
	a := core.Account{ID: "cl-1", Kind: core.KindClaude, ConfigDir: t.TempDir(), TokenAt: time.Now()}
	if Ready(s, a) {
		t.Fatal("ready without a saved token")
	}
	if err := SaveToken(a.ID, sampleToken); err != nil {
		t.Fatal(err)
	}
	if !Ready(s, a) {
		t.Fatal("not ready with a fresh token")
	}
	if tok, _ := ReadToken(a.ID); tok != sampleToken {
		t.Fatalf("read back %q", tok)
	}
	a.TokenAt = time.Now().Add(-TokenLifetime - time.Hour)
	if Ready(s, a) {
		t.Fatal("ready with a token that expired an hour ago")
	}
}

func TestTokenDaysLeftCountsTheDayItIsIn(t *testing.T) {
	now := time.Now()
	for _, c := range []struct {
		saved time.Duration
		want  int
	}{
		{0, 365},
		{-TokenLifetime + time.Hour, 1},
		{-TokenLifetime - time.Hour, -1},
	} {
		a := core.Account{TokenAt: now.Add(c.saved)}
		if got := TokenDaysLeft(a, now); got != c.want {
			t.Errorf("saved %v ago: %d days left, want %d", -c.saved, got, c.want)
		}
	}
}
