// Package relay points the official clients at the gcccode relay once every
// subscription account is exhausted. It does not impersonate the official
// login; it uses the relay token the user configured.
package relay

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nearmetips/DailiCode/internal/claude"
	"github.com/nearmetips/DailiCode/internal/core"
	"github.com/nearmetips/DailiCode/internal/lcu"
)

const (
	begin  = "# BEGIN gcccode relay\n"
	end    = "# END gcccode relay\n"
	envKey = "GCCCODE_RELAY_KEY"
)

// Ready reports whether a relay can actually take traffic.
func Ready(r core.Relay) bool {
	return r.Enabled && strings.TrimSpace(r.Token) != "" && (r.AnthropicURL != "" || r.OpenAIURL != "")
}

func profileDir() string { return filepath.Join(core.Root(), "relay-profile") }

// PrepareClaude builds a CLAUDE_CONFIG_DIR whose settings.json only contains
// the relay and LCU's entries, and whose conversations stay linked to the
// real Claude home.
func PrepareClaude(s core.Settings) (string, error) {
	r := s.Relay
	if r.AnthropicURL == "" || r.Token == "" {
		return "", fmt.Errorf("中转需要 Anthropic 地址和 token（gcccode relay set）")
	}
	dir := profileDir()
	home := core.ClaudeHome(s)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	for _, name := range []string{"projects", "plans", "skills", "commands", "agents"} {
		target := filepath.Join(home, name)
		_ = os.MkdirAll(target, 0o755)
		link := filepath.Join(dir, name)
		if _, err := os.Lstat(link); err != nil {
			_ = os.Symlink(target, link)
		}
	}
	settings := map[string]any{
		"env": map[string]any{
			"ANTHROPIC_BASE_URL":   r.AnthropicURL,
			"ANTHROPIC_AUTH_TOKEN": r.Token,
		},
	}
	if main, err := readJSONMap(filepath.Join(home, "settings.json")); err == nil {
		lcu.CopyClaudeSettings(settings, main)
	}
	b, _ := jsonMarshal(settings)
	if err := core.WriteFileAtomic(filepath.Join(dir, "settings.json"), b, 0o600); err != nil {
		return "", err
	}
	if err := carryLCUServer(s, dir); err != nil {
		return "", err
	}
	return dir, nil
}

// carryLCUServer keeps the relay dir's lcu MCP entry in step with the main
// config, so computer use survives falling back to the relay.
func carryLCUServer(s core.Settings, dir string) error {
	main, _ := readJSONMap(claude.MainConfigJSON(s))
	path := claude.ConfigJSON(s, dir)
	cfg, err := readJSONMap(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("%s 不是合法 JSON: %w", path, err)
	}
	if cfg == nil {
		cfg = map[string]any{}
	}
	if !lcu.CopyClaudeServer(cfg, main) {
		return nil
	}
	b, err := jsonMarshal(cfg)
	if err != nil {
		return err
	}
	return core.WriteFileAtomic(path, b, 0o600)
}

// ClaudeEnv returns the child environment for a relay session.
func ClaudeEnv(base []string, dir string) []string {
	drop := map[string]bool{
		"CLAUDE_CONFIG_DIR": true, "ANTHROPIC_BASE_URL": true, "ANTHROPIC_AUTH_TOKEN": true,
		"ANTHROPIC_API_KEY": true, "CLAUDE_CODE_OAUTH_TOKEN": true,
	}
	var out []string
	for _, e := range base {
		k, _, _ := strings.Cut(e, "=")
		if !drop[k] {
			out = append(out, e)
		}
	}
	return append(out, "CLAUDE_CONFIG_DIR="+dir)
}

// ActivateCodex inserts a marked provider block at the top of config.toml.
// The returned function puts the file back.
func ActivateCodex(s core.Settings) (func() error, error) {
	r := s.Relay
	if r.OpenAIURL == "" || r.Token == "" {
		return nil, fmt.Errorf("中转需要 OpenAI 兼容地址和 token（gcccode relay set）")
	}
	path := filepath.Join(core.CodexHome(s), "config.toml")
	orig, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	next := []byte(block(r) + commentProvider(stripBlock(string(orig))))
	if err := core.WriteFileAtomic(path, next, 0o600); err != nil {
		return nil, err
	}
	return func() error {
		cur, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if string(cur) != string(next) {
			return fmt.Errorf("config.toml 在中转会话期间被改过，未自动还原，原来的内容仍以 gcccode 标记包着")
		}
		return core.WriteFileAtomic(path, orig, 0o600)
	}, nil
}

// CodexEnv adds the relay key without writing it into config.toml.
func CodexEnv(base []string, r core.Relay) []string {
	var out []string
	for _, e := range base {
		k, _, _ := strings.Cut(e, "=")
		if k != envKey {
			out = append(out, e)
		}
	}
	return append(out, envKey+"="+r.Token)
}

func block(r core.Relay) string {
	return begin +
		"model_provider = \"gcccode\"\n\n" +
		"[model_providers.gcccode]\n" +
		"name = \"GCC Code\"\n" +
		"base_url = " + quote(r.OpenAIURL) + "\n" +
		"env_key = \"" + envKey + "\"\n" +
		"wire_api = \"responses\"\n" +
		end + "\n"
}

func stripBlock(s string) string {
	for {
		i := strings.Index(s, begin)
		if i < 0 {
			return s
		}
		j := strings.Index(s[i:], end)
		if j < 0 {
			return s[:i]
		}
		s = s[:i] + s[i+j+len(end):]
	}
}

func commentProvider(s string) string {
	lines := strings.Split(s, "\n")
	section := ""
	for i, line := range lines {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "[") && strings.HasSuffix(trim, "]") {
			section = trim
			continue
		}
		if section != "" || strings.HasPrefix(trim, "#") {
			continue
		}
		if strings.HasPrefix(trim, "model_provider") {
			lines[i] = "# gcccode-saved " + line
		}
	}
	return strings.Join(lines, "\n")
}

func quote(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}
