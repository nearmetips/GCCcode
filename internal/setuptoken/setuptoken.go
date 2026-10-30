// Package setuptoken adds Claude accounts that sign in with the long-lived
// token `claude setup-token` prints. The sign-in page opens in a browser
// only that account uses, because the page authorizes whichever account the
// browser is already signed in to.
package setuptoken

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/nearmetips/DailiCode/internal/bins"
	"github.com/nearmetips/DailiCode/internal/browser"
	"github.com/nearmetips/DailiCode/internal/claude"
	"github.com/nearmetips/DailiCode/internal/core"
	"github.com/nearmetips/DailiCode/internal/launcher"
)

// ProxyEnv carries the account's egress to the browser gcccode opens for it,
// so the web sign-in leaves from the same address as the CLI.
const ProxyEnv = "GCCCODE_BROWSER_PROXY"

// NoBrowser is the BROWSER value that names nothing, which makes Claude Code
// print the sign-in link instead of opening the default browser.
const NoBrowser = "gcccode-no-browser"

// NewAccount describes the Claude account an add would create, without
// saving anything yet.
func NewAccount(st *core.Store, label, proxy string) core.Account {
	id := st.NewID(core.KindClaude, label)
	if label == "" {
		label = id
	}
	return core.Account{ID: id, Kind: core.KindClaude, Label: label, Proxy: proxy, ConfigDir: core.ClaudeProfileDir(id), CreatedAt: time.Now()}
}

// Env is the account's own environment for `claude setup-token`, with
// gcccode as the BROWSER so the sign-in page reaches the account's own
// browser. own says whether such a browser exists on this machine.
func Env(s core.Settings, acc core.Account, own bool) []string {
	var env []string
	for _, e := range launcher.ChildEnv(s, acc) {
		k, _, _ := strings.Cut(e, "=")
		if k != claude.TokenEnv && k != "BROWSER" && k != browser.ForEnv && k != ProxyEnv {
			env = append(env, e)
		}
	}
	opener := NoBrowser
	if self, err := os.Executable(); own && err == nil {
		opener = self
		env = append(env, browser.ForEnv+"="+acc.ID, ProxyEnv+"="+claude.EffectiveProxy(s, acc))
	}
	return append(env, "BROWSER="+opener)
}

// Command prepares `claude setup-token` for acc, with the account's profile
// in place. The caller wires up stdio and runs it.
func Command(ctx context.Context, s core.Settings, acc core.Account) (*exec.Cmd, error) {
	bin, err := bins.Claude(s)
	if err != nil {
		return nil, fmt.Errorf("找不到 claude，先安装 Claude Code：%w", err)
	}
	if err := claude.EnsureProfile(s, acc.ConfigDir); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, bin, "setup-token")
	cmd.Env = Env(s, acc, browser.Available())
	return cmd, nil
}

// Capture runs `claude setup-token` with its output collected and returns
// the token it printed. It blocks until the user has authorized in the
// browser or ctx ends.
func Capture(ctx context.Context, s core.Settings, acc core.Account) (string, error) {
	cmd, err := Command(ctx, s, acc)
	if err != nil {
		return "", err
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	// Claude Code quits on EOF from stdin, so keep the pipe open until it ends.
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return "", err
	}
	runErr := cmd.Run()
	_ = stdin.Close()
	tok := ExtractToken(out.String())
	if msg := claude.TokenProblem(tok); msg != "" {
		if ctx.Err() != nil {
			return "", errors.New("已取消")
		}
		tail := Tail(out.String(), 6)
		if runErr != nil && tail == "" {
			tail = runErr.Error()
		}
		if tail != "" {
			return "", fmt.Errorf("%s。claude setup-token 的输出：\n%s", msg, tail)
		}
		return "", errors.New(msg)
	}
	return tok, nil
}

// Save writes acc with its token into the pool. It refuses when another add
// has taken the id since acc was prepared.
func Save(acc core.Account, tok string) error {
	_, err := core.Mutate(func(s *core.Store) error {
		if _, taken := s.Account(acc.ID); taken {
			return fmt.Errorf("同时在加另一个号，编号 %s 被它占了。请再加一次", acc.ID)
		}
		if err := claude.SaveToken(acc.ID, tok); err != nil {
			return err
		}
		acc.TokenAt = time.Now()
		s.Accounts = append(s.Accounts, acc)
		return nil
	})
	return err
}

// Discard removes the browser and profile a failed add left behind, unless
// another add has claimed the same id since.
func Discard(id string) {
	if st, err := core.Load(); err != nil {
		return
	} else if _, taken := st.Account(id); taken {
		return
	}
	browser.Remove(id)
	_ = os.RemoveAll(core.ClaudeProfileDir(id))
}

var ansi = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\a]*(\a|\x1b\\)|\r`)

var tokenChars = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// ExtractToken finds the token in what `claude setup-token` printed. The
// token is drawn as one line, which the terminal width breaks into several;
// the continuation lines hold nothing but token characters.
func ExtractToken(output string) string {
	lines := strings.Split(ansi.ReplaceAllString(output, ""), "\n")
	const prefix = "sk-ant-oat"
	for i, line := range lines {
		line = strings.TrimSpace(line)
		idx := strings.Index(line, prefix)
		if idx < 0 {
			continue
		}
		tok := prefix + leading(line[idx+len(prefix):])
		if idx+len(tok) < len(line) {
			// Text followed the token on the same line, so it was not wrapped.
			return tok
		}
		for j := i + 1; j < len(lines); j++ {
			next := strings.TrimSpace(lines[j])
			if next == "" || !tokenChars.MatchString(next) {
				break
			}
			tok += next
		}
		return tok
	}
	return ""
}

func leading(s string) string {
	for i, r := range s {
		if !(r == '-' || r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return s[:i]
		}
	}
	return s
}

// Tail returns the last n non-empty lines of output, without the token.
func Tail(output string, n int) string {
	var keep []string
	for _, line := range strings.Split(ansi.ReplaceAllString(output, ""), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.Contains(line, "sk-ant-oat") {
			keep = append(keep, line)
		}
	}
	if len(keep) > n {
		keep = keep[len(keep)-n:]
	}
	return strings.Join(keep, "\n")
}
