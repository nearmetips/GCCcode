package core

import (
	"os"
	"path/filepath"
)

// Root is GCC Code's own config directory.
func Root() string {
	if v := os.Getenv("GCCCODE_HOME"); v != "" {
		return v
	}
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return filepath.Join(v, "gcccode")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "gcccode")
}

func AccountsFile() string { return filepath.Join(Root(), "accounts.json") }
func QuotaFile() string    { return filepath.Join(Root(), "quota.json") }
func StateFile() string    { return filepath.Join(Root(), "state.json") }
func SettingsFile() string { return filepath.Join(Root(), "settings.json") }
func BackupDir() string    { return filepath.Join(Root(), "backups") }
func HandoffDir() string   { return filepath.Join(Root(), "handoffs") }

// GatewayPIDFile records the running gateway process; GatewayLogFile is its output.
func GatewayPIDFile() string { return filepath.Join(Root(), "gateway.pid") }
func GatewayLogFile() string { return filepath.Join(Root(), "gateway.log") }

// GatewayProfileDir is the config dir an official client runs on when
// use-gateway sends it through the gateway. It never holds a login.
func GatewayProfileDir(kind string) string { return filepath.Join(Root(), "gateway", kind) }

// ClaudeProfileDir is the CLAUDE_CONFIG_DIR used for one Claude account.
func ClaudeProfileDir(id string) string { return filepath.Join(Root(), "claude", id) }

// CodexVaultDir holds one Codex account's auth.json snapshot.
func CodexVaultDir(id string) string { return filepath.Join(Root(), "codex", id) }

// GeminiProfileDir is the GEMINI_CONFIG_DIR used for one Gemini account.
func GeminiProfileDir(id string) string { return filepath.Join(Root(), "gemini", id) }

// GrokProfileDir is the GROK_HOME used for one Grok account.
func GrokProfileDir(id string) string { return filepath.Join(Root(), "grok", id) }

// ClaudeHome is the user's real Claude config dir, shared across accounts.
func ClaudeHome(s Settings) string {
	if s.ClaudeHome != "" {
		return s.ClaudeHome
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude")
}

// CodexHome is the single shared Codex home.
func CodexHome(s Settings) string {
	if s.CodexHome != "" {
		return s.CodexHome
	}
	if v := os.Getenv("CODEX_HOME"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex")
}

// GeminiHome is the user's real Gemini CLI config dir (main import source).
func GeminiHome(s Settings) string {
	if s.GeminiHome != "" {
		return s.GeminiHome
	}
	if v := os.Getenv("GEMINI_CONFIG_DIR"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	xdg := filepath.Join(home, ".config", "gemini-cli")
	legacy := filepath.Join(home, ".gemini")
	if fi, err := os.Stat(xdg); err == nil && fi.IsDir() {
		return xdg
	}
	return legacy
}

// GrokHome is the user's real Grok CLI home (main import source).
func GrokHome(s Settings) string {
	if s.GrokHome != "" {
		return s.GrokHome
	}
	if v := os.Getenv("GROK_HOME"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".grok")
}

// AgyHome is the user's real HOME, where Antigravity keeps ~/.gemini for the
// main login.
func AgyHome(s Settings) string {
	if s.AgyHome != "" {
		return s.AgyHome
	}
	home, _ := os.UserHomeDir()
	return home
}

func ensureDir(p string) error { return os.MkdirAll(p, 0o700) }

// SamePath reports whether a and b name one directory. The official clients
// record the resolved cwd (/private/tmp on macOS) while $PWD may not be.
func SamePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	return err1 == nil && err2 == nil && ra == rb
}
