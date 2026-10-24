// Package bins finds the official Claude Code, Codex, Gemini, Grok and
// Antigravity executables.
package bins

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/nearmetips/DailiCode/internal/core"
)

func Claude(s core.Settings) (string, error) {
	if s.ClaudeBin != "" {
		return s.ClaudeBin, nil
	}
	return look("claude",
		filepath.Join(home(), ".local", "bin", "claude"),
		filepath.Join(home(), ".claude", "local", "claude"),
	)
}

func Codex(s core.Settings) (string, error) {
	if s.CodexBin != "" {
		return s.CodexBin, nil
	}
	var candidates []string
	if runtime.GOOS == "darwin" {
		candidates = append(candidates, "/Applications/ChatGPT.app/Contents/Resources/codex-cli/CodexCLI.app/Contents/MacOS/codex")
	}
	candidates = append(candidates,
		filepath.Join(home(), ".local", "bin", "codex"),
		filepath.Join(home(), ".codex", "bin", "codex"),
	)
	return look("codex", candidates...)
}

func Gemini(s core.Settings) (string, error) {
	if s.GeminiBin != "" {
		return s.GeminiBin, nil
	}
	return look("gemini",
		filepath.Join(home(), ".local", "bin", "gemini"),
		filepath.Join(home(), ".gemini", "bin", "gemini"),
	)
}

func Grok(s core.Settings) (string, error) {
	if s.GrokBin != "" {
		return s.GrokBin, nil
	}
	return look("grok",
		filepath.Join(home(), ".local", "bin", "grok"),
		filepath.Join(home(), ".grok", "bin", "grok"),
	)
}

func Agy(s core.Settings) (string, error) {
	if s.AgyBin != "" {
		return s.AgyBin, nil
	}
	return look("agy", filepath.Join(home(), ".local", "bin", "agy"))
}

func home() string {
	h, _ := os.UserHomeDir()
	return h
}

func look(name string, extra ...string) (string, error) {
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	for _, p := range extra {
		if isExec(p) {
			return p, nil
		}
	}
	return "", exec.ErrNotFound
}

func isExec(p string) bool {
	fi, err := os.Stat(p)
	if err != nil || fi.IsDir() {
		return false
	}
	return fi.Mode()&0o111 != 0
}
