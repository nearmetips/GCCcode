// Package lcu connects LCU (github.com/amontlabs/lcu), which runs Codex's
// computer-use runtime as a standalone MCP server, to the account pool.
//
// LCU registers itself only in the default Claude home and refuses a
// redirected CLAUDE_CONFIG_DIR, so its setup always runs against the main
// home. Account dirs pick the registration up from there: the MCP entry
// through the synced .claude.json, the lifecycle hooks and deny rules through
// the copied settings.json, and the lcu-approve mod through the linked skills
// dir. Codex accounts share one CODEX_HOME, so they need nothing extra.
package lcu

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/nearmetips/DailiCode/internal/core"
)

// Server is the MCP server name LCU registers. Its hooks, permission rules
// and approval mod all refer to the server by this name.
const Server = "lcu"

// Command returns the installed lcu launcher. LCU never adds it to PATH.
func Command() (string, bool) {
	for _, p := range candidates() {
		if isExec(p) {
			return p, true
		}
	}
	return "", false
}

func candidates() []string {
	home, _ := os.UserHomeDir()
	var out []string
	if p := os.Getenv("LCU_PREFIX"); p != "" {
		out = append(out, launcher(p))
	}
	if runtime.GOOS == "linux" {
		out = append(out, launcher("/opt/lcu"))
	}
	return append(out, launcher(filepath.Join(home, ".local", "share", "lcu")))
}

func launcher(prefix string) string { return filepath.Join(prefix, "current", "bin", "lcu") }

// Version reads the release's bundle.json, so it works without starting Python.
func Version(cmd string) string {
	f, err := os.Open(filepath.Join(filepath.Dir(filepath.Dir(cmd)), "bundle.json"))
	if err != nil {
		return ""
	}
	defer f.Close()
	var b struct {
		Version string `json:"version"`
	}
	if json.NewDecoder(f).Decode(&b) != nil {
		return ""
	}
	return b.Version
}

// Env is the environment lcu runs with. CLAUDE_CONFIG_DIR is set whenever
// gcccode runs inside a pooled Claude session, and LCU refuses it; the
// registration belongs in the main home anyway.
func Env(base []string, s core.Settings) []string {
	var out []string
	for _, e := range base {
		k, _, _ := strings.Cut(e, "=")
		if k == "CLAUDE_CONFIG_DIR" || (s.CodexHome != "" && k == "CODEX_HOME") {
			continue
		}
		out = append(out, e)
	}
	if s.CodexHome != "" {
		out = append(out, "CODEX_HOME="+s.CodexHome)
	}
	return out
}

// DefaultHome reports whether the pool's Claude home is ~/.claude, the only
// one LCU registers in.
func DefaultHome(s core.Settings) bool {
	home, _ := os.UserHomeDir()
	return filepath.Clean(core.ClaudeHome(s)) == filepath.Join(home, ".claude")
}

// SetupArgs prepends --agent for each harness unless the user already chose
// some, or asked for a mode that takes no agent.
func SetupArgs(args, agents []string) []string {
	for _, a := range args {
		name, _, _ := strings.Cut(a, "=")
		switch name {
		case "--agent", "--reconcile", "--export", "--list-agents", "-h", "--help":
			return args
		}
	}
	var out []string
	for _, a := range agents {
		out = append(out, "--agent", a)
	}
	return append(out, args...)
}

// Run executes bin in the foreground with the caller's terminal, so LCU's own
// prompts and desktop-permission guide reach the user.
func Run(bin string, args, env []string) error {
	cmd := exec.Command(bin, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = env
	return cmd.Run()
}

func isExec(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
