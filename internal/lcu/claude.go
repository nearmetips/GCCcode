package lcu

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
)

const toolPrefix = "mcp__" + Server

// Claude is what one Claude config dir carries of LCU's registration.
type Claude struct {
	Server bool // mcpServers.lcu in .claude.json
	Hooks  bool // lifecycle hooks calling the lcu server in settings.json
}

// InspectClaude reads a config dir's global config and settings.json.
func InspectClaude(configJSON, settingsJSON string) Claude {
	servers, _ := readMap(configJSON)["mcpServers"].(map[string]any)
	_, server := servers[Server]
	return Claude{Server: server, Hooks: len(hookGroups(readMap(settingsJSON))) > 0}
}

// ModInstalled reports whether the lcu-approve mod is in home's skills dir.
// Account dirs link skills to the main home, so one copy serves the pool.
func ModInstalled(claudeHome string) bool {
	return fileExists(filepath.Join(claudeHome, "skills", "lcu-approve", ".claude-plugin", "plugin.json"))
}

// CodexRegistered reports whether config.toml in codexHome has the lcu server.
func CodexRegistered(codexHome string) bool {
	f, err := os.Open(filepath.Join(codexHome, "config.toml"))
	if err != nil {
		return false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		for _, table := range []string{"mcp_servers." + Server, `mcp_servers."` + Server + `"`} {
			if line == "["+table+"]" || strings.HasPrefix(line, "["+table+".") {
				return true
			}
		}
	}
	return false
}

// CopyClaudeSettings adds LCU's lifecycle hooks and permission rules from src
// to dst. It is for config dirs that do not copy the whole main settings.json.
func CopyClaudeSettings(dst, src map[string]any) {
	if groups := hookGroups(src); len(groups) > 0 {
		hooks, _ := dst["hooks"].(map[string]any)
		if hooks == nil {
			hooks = map[string]any{}
			dst["hooks"] = hooks
		}
		for event, g := range groups {
			cur, _ := hooks[event].([]any)
			hooks[event] = append(cur, g...)
		}
	}
	for _, kind := range []string{"allow", "deny"} {
		rules := toolRules(src, kind)
		if len(rules) == 0 {
			continue
		}
		perms, _ := dst["permissions"].(map[string]any)
		if perms == nil {
			perms = map[string]any{}
			dst["permissions"] = perms
		}
		cur, _ := perms[kind].([]any)
		for _, r := range rules {
			cur = append(cur, r)
		}
		perms[kind] = cur
	}
}

// CopyClaudeServer makes dst's lcu MCP entry match src: copied when src has
// one, removed when it does not. It reports whether dst changed.
func CopyClaudeServer(dst, src map[string]any) bool {
	from, _ := src["mcpServers"].(map[string]any)
	entry, want := from[Server]
	servers, _ := dst["mcpServers"].(map[string]any)
	cur, have := servers[Server]
	switch {
	case want && have && reflect.DeepEqual(cur, entry):
		return false
	case want:
		if servers == nil {
			servers = map[string]any{}
			dst["mcpServers"] = servers
		}
		servers[Server] = entry
		return true
	case have:
		delete(servers, Server)
		return true
	}
	return false
}

// hookGroups returns, per event, the hook groups whose handlers call the lcu
// server, keeping only those handlers.
func hookGroups(m map[string]any) map[string][]any {
	hooks, _ := m["hooks"].(map[string]any)
	out := map[string][]any{}
	for event, v := range hooks {
		groups, _ := v.([]any)
		for _, g := range groups {
			group, _ := g.(map[string]any)
			handlers, _ := group["hooks"].([]any)
			var mine []any
			for _, h := range handlers {
				if hm, _ := h.(map[string]any); hm["server"] == Server {
					mine = append(mine, h)
				}
			}
			if len(mine) == 0 {
				continue
			}
			copied := map[string]any{}
			for k, v := range group {
				copied[k] = v
			}
			copied["hooks"] = mine
			out[event] = append(out[event], copied)
		}
	}
	return out
}

func toolRules(m map[string]any, kind string) []string {
	perms, _ := m["permissions"].(map[string]any)
	list, _ := perms[kind].([]any)
	var out []string
	for _, r := range list {
		if s, ok := r.(string); ok && (s == toolPrefix || strings.HasPrefix(s, toolPrefix+"__")) {
			out = append(out, s)
		}
	}
	return out
}

func readMap(path string) map[string]any {
	var m map[string]any
	if b, err := os.ReadFile(path); err == nil {
		json.Unmarshal(b, &m)
	}
	return m
}
