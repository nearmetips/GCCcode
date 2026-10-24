// Package grok isolates Grok CLI accounts under per-account GROK_HOME dirs.
package grok

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nearmetips/DailiCode/internal/core"
)

// Shared entries stay linked to the main ~/.grok so skills and plugins travel
// with every account. Auth and sessions stay private to the profile.
var sharedNames = []string{"skills", "plugins", "docs", "bundled", "completions", "installed-plugins"}

// EnsureProfile creates or repairs one account's GROK_HOME.
func EnsureProfile(s core.Settings, dir string) error {
	if IsMainHome(s, dir) {
		return nil
	}
	home := core.GrokHome(s)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(home, 0o755); err != nil {
		return err
	}
	for _, name := range sharedNames {
		target := filepath.Join(home, name)
		if _, err := os.Lstat(target); err != nil {
			continue
		}
		if err := ensureLink(filepath.Join(dir, name), target); err != nil {
			return fmt.Errorf("链接 %s: %w", name, err)
		}
	}
	for _, name := range []string{"sessions", "logs", "downloads"} {
		if err := os.MkdirAll(filepath.Join(dir, name), 0o700); err != nil {
			return err
		}
	}
	cfg := filepath.Join(dir, "config.toml")
	if _, err := os.Stat(cfg); errors.Is(err, os.ErrNotExist) {
		if b, err := os.ReadFile(filepath.Join(home, "config.toml")); err == nil {
			_ = os.WriteFile(cfg, b, 0o600)
		}
	}
	return nil
}

func IsMainHome(s core.Settings, dir string) bool {
	return filepath.Clean(dir) == filepath.Clean(core.GrokHome(s))
}

func ensureLink(link, target string) error {
	fi, err := os.Lstat(link)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return os.Symlink(target, link)
	case err != nil:
		return err
	case fi.Mode()&os.ModeSymlink != 0:
		cur, err := os.Readlink(link)
		if err == nil && filepath.Clean(cur) == filepath.Clean(target) {
			return nil
		}
		if err := os.Remove(link); err != nil {
			return err
		}
		return os.Symlink(target, link)
	default:
		return nil
	}
}

// Identity is what auth.json records for the signed-in user.
type Identity struct {
	Email  string
	UserID string
	TeamID string
}

// ReadIdentity reads the first auth entry that looks signed in.
func ReadIdentity(dir string) (Identity, bool) {
	b, err := os.ReadFile(filepath.Join(dir, "auth.json"))
	if err != nil {
		return Identity{}, false
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(b, &m) != nil {
		return Identity{}, false
	}
	for _, raw := range m {
		var e struct {
			Email  string `json:"email"`
			UserID string `json:"user_id"`
			TeamID string `json:"team_id"`
			Key    string `json:"key"`
		}
		if json.Unmarshal(raw, &e) != nil {
			continue
		}
		if e.UserID == "" && e.Email == "" && e.Key == "" {
			continue
		}
		return Identity{Email: e.Email, UserID: e.UserID, TeamID: e.TeamID}, true
	}
	return Identity{}, false
}

// Ready reports whether dir has a usable login.
func Ready(dir string) bool {
	_, ok := ReadIdentity(dir)
	return ok
}

// AuthPath is where Grok stores credentials inside a home.
func AuthPath(dir string) string { return filepath.Join(dir, "auth.json") }

// HasAuthFile is true when auth.json exists (even before we can parse it).
func HasAuthFile(dir string) bool {
	fi, err := os.Stat(AuthPath(dir))
	return err == nil && fi.Size() > 2
}

// ProxyProblem is why Grok should not use proxy as written, or "".
// Grok's HTTP stack accepts http(s); SOCKS is unreliable across versions.
func ProxyProblem(proxy string) string {
	p := strings.TrimSpace(proxy)
	if p == "" {
		return ""
	}
	lower := strings.ToLower(p)
	if strings.HasPrefix(lower, "socks") {
		return "Grok 出口请用 http:// 或 https:// 代理（SOCKS 不保证可用）"
	}
	if !strings.Contains(p, "://") {
		return "代理地址要带协议，例如 http://127.0.0.1:7890"
	}
	return ""
}
