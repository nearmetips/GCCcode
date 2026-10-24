// Package gemini isolates Gemini CLI accounts under per-account GEMINI_CONFIG_DIR dirs.
package gemini

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nearmetips/DailiCode/internal/core"
)

// Shared entries stay linked to the main Gemini config so skills and MCP
// travel with every account. Auth and chat history stay private.
var sharedNames = []string{"skills", "extensions", "commands", "plugins"}

// EnsureProfile creates or repairs one account's GEMINI_CONFIG_DIR.
func EnsureProfile(s core.Settings, dir string) error {
	if IsMainHome(s, dir) {
		return nil
	}
	home := core.GeminiHome(s)
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
	for _, name := range []string{"tmp", "history", "projects"} {
		if err := os.MkdirAll(filepath.Join(dir, name), 0o700); err != nil {
			return err
		}
	}
	settings := filepath.Join(dir, "settings.json")
	if _, err := os.Stat(settings); errors.Is(err, os.ErrNotExist) {
		if b, err := os.ReadFile(filepath.Join(home, "settings.json")); err == nil {
			_ = os.WriteFile(settings, filterProxySettings(b), 0o600)
		} else {
			_ = os.WriteFile(settings, []byte("{}\n"), 0o600)
		}
	}
	return nil
}

// UseAPIKeyAuth makes the profile in dir authenticate with GEMINI_API_KEY,
// the only way Gemini CLI follows GOOGLE_GEMINI_BASE_URL. Settings in the
// old flat layout keep that layout.
func UseAPIKeyAuth(dir string) error {
	const apiKey = "gemini-api-key"
	path := filepath.Join(dir, "settings.json")
	m := map[string]any{}
	if b, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(b))) > 0 {
		if err := json.Unmarshal(b, &m); err != nil {
			return fmt.Errorf("%s 不是合法 JSON: %w", path, err)
		}
	}
	if cur, flat := m["selectedAuthType"]; flat {
		if cur == apiKey {
			return nil
		}
		m["selectedAuthType"] = apiKey
	} else {
		security, _ := m["security"].(map[string]any)
		if security == nil {
			security = map[string]any{}
		}
		auth, _ := security["auth"].(map[string]any)
		if auth == nil {
			auth = map[string]any{}
		}
		if auth["selectedType"] == apiKey {
			return nil
		}
		auth["selectedType"] = apiKey
		security["auth"] = auth
		m["security"] = security
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return core.WriteFileAtomic(path, append(b, '\n'), 0o600)
}

func IsMainHome(s core.Settings, dir string) bool {
	return filepath.Clean(dir) == filepath.Clean(core.GeminiHome(s))
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

func filterProxySettings(b []byte) []byte {
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return b
	}
	if env, ok := m["env"].(map[string]any); ok {
		for _, k := range []string{"https_proxy", "HTTPS_PROXY", "http_proxy", "HTTP_PROXY", "ALL_PROXY", "all_proxy"} {
			delete(env, k)
		}
		if len(env) == 0 {
			delete(m, "env")
		}
	}
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return b
	}
	return append(out, '\n')
}

// Identity is what Gemini CLI recorded for the signed-in Google account.
type Identity struct {
	Email string
	ID    string
}

// ReadIdentity looks for common credential files Gemini CLI writes.
func ReadIdentity(dir string) (Identity, bool) {
	for _, name := range []string{
		"google_accounts.json",
		"oauth_creds.json",
		filepath.Join("oauth", "credentials.json"),
	} {
		if idty, ok := identityFromFile(filepath.Join(dir, name)); ok {
			return idty, true
		}
	}
	// A selected auth type in settings plus any non-empty .env counts as ready
	// for API-key logins that never write google_accounts.json.
	if hasAPIKey(dir) {
		return Identity{Email: "api-key"}, true
	}
	return Identity{}, false
}

func identityFromFile(path string) (Identity, bool) {
	b, err := os.ReadFile(path)
	if err != nil || len(strings.TrimSpace(string(b))) == 0 {
		return Identity{}, false
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		// Non-JSON token files still mean a login happened.
		return Identity{}, true
	}
	email, _ := m["email"].(string)
	if email == "" {
		email, _ = m["emailAddress"].(string)
	}
	if email == "" {
		if active, _ := m["active"].(string); active != "" {
			email = active
		}
	}
	if accounts, ok := m["accounts"].([]any); ok && email == "" {
		for _, a := range accounts {
			am, _ := a.(map[string]any)
			if e, _ := am["email"].(string); e != "" {
				email = e
				break
			}
		}
	}
	id, _ := m["id"].(string)
	if id == "" {
		id, _ = m["sub"].(string)
	}
	if email == "" && id == "" && m["access_token"] == nil && m["refresh_token"] == nil {
		return Identity{}, len(m) > 0
	}
	return Identity{Email: email, ID: id}, true
}

func hasAPIKey(dir string) bool {
	for _, name := range []string{".env", "settings.json"} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		s := string(b)
		if strings.Contains(s, "GEMINI_API_KEY") || strings.Contains(s, "GOOGLE_API_KEY") {
			return true
		}
	}
	return false
}

// Ready reports whether dir has a usable login or API key.
func Ready(dir string) bool {
	_, ok := ReadIdentity(dir)
	return ok
}

// ProxyProblem is why Gemini CLI should not use proxy as written, or "".
func ProxyProblem(proxy string) string {
	p := strings.TrimSpace(proxy)
	if p == "" {
		return ""
	}
	lower := strings.ToLower(p)
	if strings.HasPrefix(lower, "socks") {
		return "Gemini 出口请用 http:// 或 https:// 代理（SOCKS 不保证可用）"
	}
	if !strings.Contains(p, "://") {
		return "代理地址要带协议，例如 http://127.0.0.1:7890"
	}
	return ""
}

// AccountByDir maps a GEMINI_CONFIG_DIR back to a pool account.
func AccountByDir(st *core.Store, dir string) (*core.Account, bool) {
	if dir == "" {
		return nil, false
	}
	clean := filepath.Clean(dir)
	for i := range st.Accounts {
		a := &st.Accounts[i]
		if a.Kind == core.KindGemini && filepath.Clean(a.ConfigDir) == clean {
			return a, true
		}
	}
	return nil, false
}
