// Package claude manages one CLAUDE_CONFIG_DIR per Claude account.
//
// Claude Code keys its keychain entry on a hash of CLAUDE_CONFIG_DIR, so each
// account dir holds its own login. History and user content are symlinked
// back to the real ~/.claude so switching accounts never loses a conversation.
package claude

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/nearmetips/DailiCode/internal/core"
)

// KeychainService is the macOS keychain item Claude Code uses for dir.
// Confirmed against Claude Code 2.1.143: the suffix is the first 8 hex
// characters of SHA-256(CLAUDE_CONFIG_DIR) when that variable is set.
func KeychainService(dir string) string {
	if dir == "" {
		return "Claude Code-credentials"
	}
	sum := sha256.Sum256([]byte(dir))
	return "Claude Code-credentials-" + hex.EncodeToString(sum[:])[:8]
}

// sharedDirs and sharedFiles are linked to the real Claude home. Everything
// else in the account dir (.claude.json, credentials, caches, telemetry) is
// per account.
var sharedDirs = []string{
	"projects", "sessions", "todos", "plans", "tasks", "file-history",
	"skills", "agents", "commands", "plugins", "rules",
	"themes", "output-styles", "ide",
}

// privateDirs used to be shared. They hold per-launch environment and caches
// that would stamp every account with the same machine, so each profile keeps
// its own copy.
var privateDirs = []string{"session-env", "telemetry", "cache"}

var sharedFiles = []string{"history.jsonl", "CLAUDE.md", "keybindings.json", "loop.md"}

// authEnvKeys would point a subscription account at an API key or relay
// instead of its own login, so they never reach an account's settings.json.
var authEnvKeys = []string{
	"ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY",
	"CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX",
	"CLAUDE_CODE_USE_FOUNDRY", "ANTHROPIC_BEDROCK_BASE_URL", "ANTHROPIC_VERTEX_PROJECT_ID",
}

// relayModelKeys only make sense next to a relay base URL.
var relayModelKeys = []string{
	"ANTHROPIC_MODEL", "ANTHROPIC_SMALL_FAST_MODEL", "ANTHROPIC_DEFAULT_OPUS_MODEL",
	"ANTHROPIC_DEFAULT_SONNET_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL",
}

// proxyEnvKeys are the proxy variables, in the order Claude Code looks them
// up. A settings.json env value overrides the process environment, so one
// copied into every account would put them all on the same egress.
var proxyEnvKeys = []string{"https_proxy", "HTTPS_PROXY", "http_proxy", "HTTP_PROXY", "ALL_PROXY", "all_proxy"}

// mainConfigJSON mirrors Claude's own lookup of its global config file.
func mainConfigJSON(home string, isDefaultHome bool) string {
	if p := filepath.Join(home, ".config.json"); fileExists(p) {
		return p
	}
	if isDefaultHome {
		h, _ := os.UserHomeDir()
		return filepath.Join(h, ".claude.json")
	}
	return filepath.Join(home, ".claude.json")
}

func profileConfigJSON(dir string) string {
	if p := filepath.Join(dir, ".config.json"); fileExists(p) {
		return p
	}
	return filepath.Join(dir, ".claude.json")
}

// MainConfigJSON is the global config file of the user's own Claude home.
func MainConfigJSON(s core.Settings) string {
	home := core.ClaudeHome(s)
	return mainConfigJSON(home, filepath.Clean(home) == filepath.Clean(defaultClaudeHome()))
}

// ConfigJSON is the global config file Claude reads when launched on dir.
func ConfigJSON(s core.Settings, dir string) string {
	if IsMainHome(s, dir) {
		return MainConfigJSON(s)
	}
	return profileConfigJSON(dir)
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// IsMainHome reports whether dir is the user's own Claude home, which is
// launched without CLAUDE_CONFIG_DIR so its default keychain entry is used.
func IsMainHome(s core.Settings, dir string) bool {
	return filepath.Clean(dir) == filepath.Clean(core.ClaudeHome(s))
}

// EnsureProfile creates or repairs an account dir: shared links, filtered
// settings and the MCP servers from the main config.
func EnsureProfile(s core.Settings, dir string) error {
	if IsMainHome(s, dir) {
		return nil
	}
	home := core.ClaudeHome(s)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(home, 0o755); err != nil {
		return err
	}
	for _, name := range sharedDirs {
		target := filepath.Join(home, name)
		if err := os.MkdirAll(target, 0o755); err != nil {
			return err
		}
		if err := ensureLink(filepath.Join(dir, name), target, true); err != nil {
			return fmt.Errorf("链接 %s: %w", name, err)
		}
	}
	for _, name := range sharedFiles {
		target := filepath.Join(home, name)
		if err := ensureLink(filepath.Join(dir, name), target, false); err != nil {
			return fmt.Errorf("链接 %s: %w", name, err)
		}
	}
	if err := SyncSettings(s, dir); err != nil {
		return fmt.Errorf("同步 settings.json: %w", err)
	}
	if err := syncMCP(s, dir); err != nil {
		return fmt.Errorf("同步 MCP 配置: %w", err)
	}
	for _, name := range privateDirs {
		if err := unsharePrivate(filepath.Join(dir, name)); err != nil {
			return fmt.Errorf("分开 %s: %w", name, err)
		}
	}
	return nil
}

// unsharePrivate turns a symlink back into a private directory. The shared
// target is left in place.
func unsharePrivate(link string) error {
	fi, err := os.Lstat(link)
	if errors.Is(err, os.ErrNotExist) {
		return os.MkdirAll(link, 0o700)
	}
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		if fi.IsDir() {
			return nil
		}
		return nil
	}
	if err := os.Remove(link); err != nil {
		return err
	}
	return os.MkdirAll(link, 0o700)
}

// ensureLink makes link a symlink to target. A real file or dir left at link
// (for example after an editor replaced the symlink by renaming over it) is
// merged into target first, so nothing written there is lost.
func ensureLink(link, target string, isDir bool) error {
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
	case fi.IsDir():
		if err := mergeDir(link, target); err != nil {
			return err
		}
		if err := os.RemoveAll(link); err != nil {
			return err
		}
		return os.Symlink(target, link)
	default:
		if err := mergeFile(link, target); err != nil {
			return err
		}
		if err := os.Remove(link); err != nil {
			return err
		}
		return os.Symlink(target, link)
	}
}

func mergeDir(src, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		from := filepath.Join(src, e.Name())
		to := filepath.Join(dst, e.Name())
		if e.IsDir() {
			if err := mergeDir(from, to); err != nil {
				return err
			}
			continue
		}
		if err := mergeFile(from, to); err != nil {
			return err
		}
	}
	return nil
}

// mergeFile moves src into dst. JSONL logs are merged line by line; for any
// other file the newer copy wins and the older one is kept as a backup.
func mergeFile(src, dst string) error {
	dfi, err := os.Stat(dst)
	if errors.Is(err, os.ErrNotExist) {
		return os.Rename(src, dst)
	}
	if err != nil {
		return err
	}
	sfi, err := os.Stat(src)
	if err != nil {
		return err
	}
	if strings.HasSuffix(src, ".jsonl") {
		return appendMissingLines(src, dst)
	}
	if sfi.ModTime().After(dfi.ModTime()) {
		backup := dst + ".gcccode-bak-" + time.Now().Format("20060102-150405")
		if err := copyFile(dst, backup); err != nil {
			return err
		}
		return copyFile(src, dst)
	}
	return nil
}

func appendMissingLines(src, dst string) error {
	have, err := os.ReadFile(dst)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, l := range strings.Split(string(have), "\n") {
		seen[l] = true
	}
	add, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	var extra []string
	for _, l := range strings.Split(string(add), "\n") {
		if l != "" && !seen[l] {
			extra = append(extra, l)
		}
	}
	if len(extra) == 0 {
		return nil
	}
	f, err := os.OpenFile(dst, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if len(have) > 0 && have[len(have)-1] != '\n' {
		f.WriteString("\n")
	}
	_, err = f.WriteString(strings.Join(extra, "\n") + "\n")
	return err
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	fi, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, fi.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// SyncSettings writes the account's settings.json from the main one, without
// any env that would bypass the account's own login or egress. The launcher
// passes the proxy instead (see EffectiveProxy). The statusLine set by
// `gcccode integrate` comes along with everything else.
func SyncSettings(s core.Settings, dir string) error {
	main := filepath.Join(core.ClaudeHome(s), "settings.json")
	var m map[string]any
	if b, err := os.ReadFile(main); err == nil && len(strings.TrimSpace(string(b))) > 0 {
		if err := json.Unmarshal(b, &m); err != nil {
			return fmt.Errorf("%s 不是合法 JSON: %w", main, err)
		}
	}
	if m == nil {
		m = map[string]any{}
	}
	if env, ok := m["env"].(map[string]any); ok {
		for _, k := range proxyEnvKeys {
			delete(env, k)
		}
	}
	FilterAuth(m)
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return core.WriteFileAtomic(filepath.Join(dir, "settings.json"), append(b, '\n'), 0o600)
}

// MarkOnboarded lets a profile that never logs in, the gateway's, skip
// Claude Code's first-run screens. The theme comes from the main config.
func MarkOnboarded(s core.Settings, dir string) error {
	path := profileConfigJSON(dir)
	m := map[string]any{}
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, &m); err != nil {
			return fmt.Errorf("%s 不是合法 JSON: %w", path, err)
		}
	}
	if done, _ := m["hasCompletedOnboarding"].(bool); done {
		return nil
	}
	m["hasCompletedOnboarding"] = true
	if _, ok := m["theme"]; !ok {
		var main map[string]any
		if b, err := os.ReadFile(MainConfigJSON(s)); err == nil && json.Unmarshal(b, &main) == nil && main["theme"] != nil {
			m["theme"] = main["theme"]
		}
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return core.WriteFileAtomic(path, append(b, '\n'), 0o600)
}

// FilterAuth drops relay and API-key settings from a settings.json map.
func FilterAuth(m map[string]any) {
	delete(m, "apiKeyHelper")
	env, ok := m["env"].(map[string]any)
	if !ok {
		return
	}
	_, relay := env["ANTHROPIC_BASE_URL"]
	for _, k := range authEnvKeys {
		delete(env, k)
	}
	if relay {
		for _, k := range relayModelKeys {
			delete(env, k)
		}
	}
	if len(env) == 0 {
		delete(m, "env")
	}
}

// SettingsProxy is the proxy the main settings.json env sets, or "".
func SettingsProxy(s core.Settings) string {
	b, err := os.ReadFile(filepath.Join(core.ClaudeHome(s), "settings.json"))
	if err != nil {
		return ""
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return ""
	}
	env, _ := m["env"].(map[string]any)
	for _, k := range proxyEnvKeys[:4] {
		if v, _ := env[k].(string); strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// EffectiveProxy is the proxy Claude Code will actually use for acc, or ""
// when it follows the launching shell. The main home reads the main
// settings.json, whose proxy wins over anything gcccode passes; every other
// account has that proxy stripped and gets acc.Proxy, or the main one when
// it has none of its own.
func EffectiveProxy(s core.Settings, acc core.Account) string {
	main := SettingsProxy(s)
	if IsMainHome(s, acc.ConfigDir) && main != "" {
		return main
	}
	if p := strings.TrimSpace(acc.Proxy); p != "" {
		return p
	}
	return main
}

// RelayInSettings reports whether the main settings.json routes Claude to a
// relay or API key, which means the main home is not a subscription login.
func RelayInSettings(s core.Settings) bool {
	b, err := os.ReadFile(filepath.Join(core.ClaudeHome(s), "settings.json"))
	if err != nil {
		return false
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return false
	}
	env, _ := m["env"].(map[string]any)
	for _, k := range []string{"ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY"} {
		if _, ok := env[k]; ok {
			return true
		}
	}
	_, helper := m["apiKeyHelper"]
	return helper
}

// syncedMCPFile lists the servers syncMCP last copied from the main config,
// so a server removed there (LCU after an uninstall, say) leaves the account
// too instead of failing to start in every later session.
const syncedMCPFile = "gcccode-mcp.json"

// syncedMCP is the content of syncedMCPFile. Older versions wrote only the
// user-scope names as a bare array.
type syncedMCP struct {
	User     []string            `json:"user"`
	Projects map[string][]string `json:"projects,omitempty"`
}

func readSyncedMCP(dir string) syncedMCP {
	b, err := os.ReadFile(filepath.Join(dir, syncedMCPFile))
	if err != nil {
		return syncedMCP{}
	}
	var names []string
	if json.Unmarshal(b, &names) == nil {
		return syncedMCP{User: names}
	}
	var out syncedMCP
	json.Unmarshal(b, &out)
	return out
}

// syncMCP copies the main config's MCP servers into the account's
// .claude.json: user scope, and local scope (what `claude mcp add` writes by
// default) under each project. Servers added only inside the account are kept.
func syncMCP(s core.Settings, dir string) error {
	var main map[string]any
	if b, err := os.ReadFile(MainConfigJSON(s)); err == nil {
		json.Unmarshal(b, &main)
	}
	servers, _ := main["mcpServers"].(map[string]any)
	local := map[string]map[string]any{}
	if projects, ok := main["projects"].(map[string]any); ok {
		for p, v := range projects {
			entry, _ := v.(map[string]any)
			if ps, _ := entry["mcpServers"].(map[string]any); len(ps) > 0 {
				local[p] = ps
			}
		}
	}
	synced := readSyncedMCP(dir)
	if len(servers) == 0 && len(local) == 0 && len(synced.User) == 0 && len(synced.Projects) == 0 {
		return nil
	}
	path := profileConfigJSON(dir)
	var prof map[string]any
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, &prof); err != nil {
			return fmt.Errorf("%s 不是合法 JSON: %w", path, err)
		}
	}
	if prof == nil {
		prof = map[string]any{}
	}
	changed := mergeServers(prof, servers, synced.User)
	projects, _ := prof["projects"].(map[string]any)
	if projects == nil {
		projects = map[string]any{}
	}
	paths := map[string]bool{}
	for p := range local {
		paths[p] = true
	}
	for p := range synced.Projects {
		paths[p] = true
	}
	for p := range paths {
		entry, _ := projects[p].(map[string]any)
		if entry == nil {
			if len(local[p]) == 0 {
				continue
			}
			entry = map[string]any{}
		}
		if mergeServers(entry, local[p], synced.Projects[p]) {
			projects[p] = entry
			prof["projects"] = projects
			changed = true
		}
	}
	if changed {
		b, err := json.MarshalIndent(prof, "", "  ")
		if err != nil {
			return err
		}
		if err := core.WriteFileAtomic(path, append(b, '\n'), 0o600); err != nil {
			return err
		}
	}
	next := syncedMCP{User: serverNames(servers)}
	for p, ps := range local {
		if next.Projects == nil {
			next.Projects = map[string][]string{}
		}
		next.Projects[p] = serverNames(ps)
	}
	b, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if old, err := os.ReadFile(filepath.Join(dir, syncedMCPFile)); err == nil && bytes.Equal(bytes.TrimSpace(old), b) {
		return nil
	}
	return core.WriteFileAtomic(filepath.Join(dir, syncedMCPFile), append(b, '\n'), 0o600)
}

// mergeServers brings holder's mcpServers in line with servers, dropping the
// ones a previous sync copied that are gone now. It reports a change.
func mergeServers(holder map[string]any, servers map[string]any, synced []string) bool {
	merged, _ := holder["mcpServers"].(map[string]any)
	if merged == nil {
		merged = map[string]any{}
	}
	changed := false
	for _, k := range synced {
		if _, still := servers[k]; still {
			continue
		}
		if _, ok := merged[k]; ok {
			delete(merged, k)
			changed = true
		}
	}
	for k, v := range servers {
		if !reflect.DeepEqual(merged[k], v) {
			merged[k] = v
			changed = true
		}
	}
	if changed {
		holder["mcpServers"] = merged
	}
	return changed
}

func serverNames(servers map[string]any) []string {
	names := make([]string, 0, len(servers))
	for k := range servers {
		names = append(names, k)
	}
	slices.Sort(names)
	return names
}

func defaultClaudeHome() string {
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".claude")
}

// Identity is what Claude records about the signed-in account.
type Identity struct {
	Email       string
	AccountUUID string
	OrgUUID     string
	OrgName     string
	BillingType string
}

// ReadIdentity reads oauthAccount from an account dir's .claude.json (or the
// main config for the main home). It never touches credentials.
func ReadIdentity(s core.Settings, dir string) (Identity, bool) {
	b, err := os.ReadFile(ConfigJSON(s, dir))
	if err != nil {
		return Identity{}, false
	}
	var cfg struct {
		OAuth *struct {
			Email       string `json:"emailAddress"`
			AccountUUID string `json:"accountUuid"`
			OrgUUID     string `json:"organizationUuid"`
			OrgName     string `json:"organizationName"`
			BillingType string `json:"billingType"`
		} `json:"oauthAccount"`
	}
	if json.Unmarshal(b, &cfg) != nil || cfg.OAuth == nil || cfg.OAuth.AccountUUID == "" {
		return Identity{}, false
	}
	return Identity{
		Email:       cfg.OAuth.Email,
		AccountUUID: cfg.OAuth.AccountUUID,
		OrgUUID:     cfg.OAuth.OrgUUID,
		OrgName:     cfg.OAuth.OrgName,
		BillingType: cfg.OAuth.BillingType,
	}, true
}

// LinkReport lists shared entries that are not symlinked to the main home.
func LinkReport(s core.Settings, dir string) []string {
	if IsMainHome(s, dir) {
		return nil
	}
	home := core.ClaudeHome(s)
	var bad []string
	for _, name := range append(append([]string{}, sharedDirs...), sharedFiles...) {
		cur, err := os.Readlink(filepath.Join(dir, name))
		if err != nil || filepath.Clean(cur) != filepath.Clean(filepath.Join(home, name)) {
			bad = append(bad, name)
		}
	}
	return bad
}
