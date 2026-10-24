// Package integrate wires GCC Code into Claude Code's status line and the shell.
package integrate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/nearmetips/DailiCode/internal/claude"
	"github.com/nearmetips/DailiCode/internal/core"
)

const statusKey = "statusLine"

// StatusCommand is the command Claude Code runs for the status line.
func StatusCommand() string {
	return shellQuote(claude.ExecutableHint()) + " statusline"
}

// IsOurs reports whether a statusLine entry runs gcccode.
func IsOurs(entry map[string]any) bool {
	cmd, _ := entry["command"].(string)
	cmd = strings.TrimSpace(cmd)
	return cmd != "" && (cmd == StatusCommand() || strings.Contains(cmd, "gcccode") && strings.HasSuffix(cmd, " statusline"))
}

// InstallStatusLine points Claude Code's status line at gcccode, in the main
// settings.json and every account profile. A status line the user already
// had is saved; it keeps showing above gcccode's line and comes back with
// RemoveStatusLine. It returns that saved command, if any.
func InstallStatusLine() (prev string, err error) {
	_, err = core.Mutate(func(st *core.Store) error {
		err := editMain(st, func(m map[string]any) bool {
			cur, _ := m[statusKey].(map[string]any)
			if !IsOurs(cur) {
				st.Settings.StatusLinePrev = cur
			}
			next := map[string]any{}
			for k, v := range cur {
				next[k] = v
			}
			next["type"] = "command"
			next["command"] = StatusCommand()
			m[statusKey] = next
			return true
		})
		prev = PreviousCommand(st.Settings)
		return err
	})
	return prev, err
}

// RemoveStatusLine puts back the status line the user had before gcccode.
// It reports false when gcccode's status line was not installed.
func RemoveStatusLine() (removed bool, err error) {
	_, err = core.Mutate(func(st *core.Store) error {
		return editMain(st, func(m map[string]any) bool {
			cur, _ := m[statusKey].(map[string]any)
			if !IsOurs(cur) {
				return false
			}
			if prev := st.Settings.StatusLinePrev; prev != nil {
				m[statusKey] = prev
			} else {
				delete(m, statusKey)
			}
			st.Settings.StatusLinePrev = nil
			removed = true
			return true
		})
	})
	return removed, err
}

// Installed reports whether the main settings.json runs gcccode's status line.
func Installed(s core.Settings) bool {
	m, err := readMain(s)
	if err != nil {
		return false
	}
	cur, _ := m[statusKey].(map[string]any)
	return IsOurs(cur)
}

// PreviousCommand is the command of the status line saved by InstallStatusLine.
func PreviousCommand(s core.Settings) string {
	prev := s.StatusLinePrev
	if prev == nil || IsOurs(prev) {
		return ""
	}
	if t, _ := prev["type"].(string); t != "" && t != "command" {
		return ""
	}
	cmd, _ := prev["command"].(string)
	return strings.TrimSpace(cmd)
}

// RunPrevious runs the saved status line with the input Claude Code sent and
// delivers its output, or nil when there is none. A slow command is cut off
// so gcccode's own line still shows.
func RunPrevious(s core.Settings, input []byte) <-chan string {
	command := PreviousCommand(s)
	if command == "" {
		return nil
	}
	out := make(chan string, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		var cmd *exec.Cmd
		if runtime.GOOS == "windows" {
			cmd = exec.CommandContext(ctx, "cmd", "/C", command)
		} else {
			cmd = exec.CommandContext(ctx, "sh", "-c", command)
		}
		cmd.Stdin = bytes.NewReader(input)
		cmd.WaitDelay = 500 * time.Millisecond
		b, _ := cmd.Output()
		out <- strings.TrimRight(string(b), "\r\n")
	}()
	return out
}

func mainSettings(s core.Settings) string {
	return filepath.Join(core.ClaudeHome(s), "settings.json")
}

func readMain(s core.Settings) (map[string]any, error) {
	path := mainSettings(s)
	m := map[string]any{}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return m, nil
		}
		return nil, err
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return m, nil
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("%s 不是合法 JSON: %w", path, err)
	}
	return m, nil
}

// editMain applies fn to the main settings.json and, when fn reports a
// change, writes it and refreshes every account profile from it.
func editMain(st *core.Store, fn func(m map[string]any) bool) error {
	m, err := readMain(st.Settings)
	if err != nil {
		return err
	}
	if !fn(m) {
		return nil
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	path := mainSettings(st.Settings)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := core.WriteFileAtomic(path, append(b, '\n'), 0o600); err != nil {
		return err
	}
	for _, a := range st.ByKind(core.KindClaude) {
		if err := claude.SyncSettings(st.Settings, a.ConfigDir); err != nil {
			return err
		}
	}
	return nil
}

// ShellInit prints functions that route every official command through GCC Code.
func ShellInit(shell string) string {
	bin := claude.ExecutableHint()
	var b strings.Builder
	for _, name := range core.Kinds {
		if shell == "fish" {
			fmt.Fprintf(&b, "function %s\n    %s %s $argv\nend\n", name, fishQuote(bin), name)
		} else {
			fmt.Fprintf(&b, "%s() { command %s %s \"$@\"; }\n", name, shellQuote(bin), name)
		}
	}
	return b.String()
}

func plainWord(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("/._-+:@%,=", r)) {
			return false
		}
	}
	return true
}

func shellQuote(s string) string {
	if plainWord(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func fishQuote(s string) string {
	if plainWord(s) {
		return s
	}
	return "'" + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), "'", `\'`) + "'"
}
