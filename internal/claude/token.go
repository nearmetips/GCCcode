package claude

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/nearmetips/DailiCode/internal/core"
)

// TokenLifetime is how long a `claude setup-token` token lasts. Such a token
// carries only the inference scope: no Claude in Chrome, no Remote Control.
const TokenLifetime = 365 * 24 * time.Hour

// TokenEnv is the variable Claude Code reads a long-lived token from, ahead
// of any keychain login.
const TokenEnv = "CLAUDE_CODE_OAUTH_TOKEN"

var tokenPattern = regexp.MustCompile(`sk-ant-[a-z]+[0-9]*-[A-Za-z0-9_-]+`)

// CleanToken finds the token in what the user pasted: a whole
// `export CLAUDE_CODE_OAUTH_TOKEN=...` line, quotes, or a token the terminal
// broke over several lines with box borders in between.
func CleanToken(pasted string) string {
	s := strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || r >= 0x2500 && r <= 0x257F || r == '"' || r == '\'' {
			return -1
		}
		return r
	}, pasted)
	if _, after, ok := strings.Cut(s, TokenEnv+"="); ok {
		s = after
	}
	return tokenPattern.FindString(s)
}

// TokenProblem says why tok cannot be a setup-token, or "" when it can.
func TokenProblem(tok string) string {
	switch {
	case tok == "":
		return "没找到 token。它以 sk-ant-oat01- 开头，是 claude setup-token 最后打印的那一长串"
	case strings.HasPrefix(tok, "sk-ant-api"):
		return "这是 API Key，不是会员 token。要的是 claude setup-token 打印的 sk-ant-oat01- 开头那串"
	case !strings.HasPrefix(tok, "sk-ant-oat"):
		return "这不像 claude setup-token 打印的 token，它以 sk-ant-oat01- 开头"
	case len(tok) < 40:
		return "token 不完整，可能只复制了一部分"
	}
	return ""
}

// ReadToken returns the account's saved setup-token.
func ReadToken(id string) (string, bool) {
	b, err := os.ReadFile(core.ClaudeTokenFile(id))
	if err != nil {
		return "", false
	}
	tok := strings.TrimSpace(string(b))
	return tok, tok != ""
}

// SaveToken stores tok readable by the user only.
func SaveToken(id, tok string) error {
	if msg := TokenProblem(tok); msg != "" {
		return errors.New(msg)
	}
	p := core.ClaudeTokenFile(id)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	return core.WriteFileAtomic(p, []byte(tok+"\n"), 0o600)
}

func RemoveToken(id string) { _ = os.Remove(core.ClaudeTokenFile(id)) }

// TokenDaysLeft is how many days, rounded up, the account's token has before
// it expires; -1 once it has.
func TokenDaysLeft(a core.Account, now time.Time) int {
	left := a.TokenAt.Add(TokenLifetime).Sub(now)
	if left <= 0 {
		return -1
	}
	return int(math.Ceil(left.Hours() / 24))
}

func usesToken(a core.Account) bool { return !a.TokenAt.IsZero() }
