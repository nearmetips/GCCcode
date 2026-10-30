package setuptoken

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nearmetips/DailiCode/internal/claude"
	"github.com/nearmetips/DailiCode/internal/core"
)

const tok = "sk-ant-oat01-AbCdEfGhIjKlMnOpQrStUvWxYz0123456789_-AbCdEfGhIjKlMnOpQrStUvWxYz-AAbbCCddEEffGGhhIIjjKKllMM-ZZ"

func TestExtractTokenReadsInkOutput(t *testing.T) {
	wrapped := "\x1b[32m✓ Long-lived authentication token created successfully!\x1b[39m\n\n" +
		"Your OAuth token (valid for 1 year):\n" +
		"\x1b[33m" + tok[:80] + "\x1b[39m\n" + tok[80:] + "   \n" +
		"\x1b[2mStore this token securely. You won't be able to see it again.\x1b[22m\n" +
		"Use this token by setting: export CLAUDE_CODE_OAUTH_TOKEN=<token>\n"
	for name, out := range map[string]string{
		"wrapped at 80 columns": wrapped,
		"one line":              "token:\n" + tok + "\nStore this token securely.\n",
		"export line":           "export CLAUDE_CODE_OAUTH_TOKEN=" + tok + "\n",
		"carriage returns":      strings.ReplaceAll(wrapped, "\n", "\r\n"),
	} {
		if got := ExtractToken(out); got != tok {
			t.Errorf("%s: got %q", name, got)
		}
	}
	if got := ExtractToken("Browser didn't open. Visit https://claude.com/cai/oauth/authorize?x=1\n"); got != "" {
		t.Errorf("found a token in output without one: %q", got)
	}
}

func TestTailDropsTheToken(t *testing.T) {
	got := Tail("a\n\n"+tok+"\nb\nc\n", 2)
	if got != "b\nc" {
		t.Fatalf("got %q", got)
	}
}

func TestCaptureRunsClaudeInTheAccountsBrowser(t *testing.T) {
	t.Setenv("GCCCODE_HOME", t.TempDir())
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "from-zshrc")
	s := core.DefaultSettings()
	s.ClaudeHome = t.TempDir()
	fake := filepath.Join(t.TempDir(), "claude")
	script := "#!/bin/sh\n" +
		"[ \"$1\" = setup-token ] || exit 2\n" +
		"[ -z \"$CLAUDE_CODE_OAUTH_TOKEN\" ] || { echo inherited token; exit 3; }\n" +
		"echo \"browser=$BROWSER for=$GCCCODE_BROWSER_FOR dir=$CLAUDE_CONFIG_DIR\" > \"$CHECK\"\n" +
		"echo 'Your OAuth token (valid for 1 year):'\n" +
		"echo '" + tok[:80] + "'\necho '" + tok[80:] + "'\n" +
		"echo 'Store this token securely.'\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	s.ClaudeBin = fake
	check := filepath.Join(t.TempDir(), "check")
	t.Setenv("CHECK", check)

	st, _ := core.Load()
	acc := NewAccount(st, "工作号", "http://127.0.0.1:7891")
	if acc.ID == "" || acc.Label != "工作号" || acc.Proxy == "" {
		t.Fatalf("account %+v", acc)
	}
	got, err := Capture(context.Background(), s, acc)
	if err != nil || got != tok {
		t.Fatalf("got %q, %v", got, err)
	}
	seen, _ := os.ReadFile(check)
	if !strings.Contains(string(seen), "for="+acc.ID) || !strings.Contains(string(seen), "dir="+acc.ConfigDir) {
		t.Fatalf("claude ran with %s", seen)
	}
	if err := Save(acc, got); err != nil {
		t.Fatal(err)
	}
	st, _ = core.Load()
	saved, ok := st.Account(acc.ID)
	if !ok || saved.TokenAt.IsZero() || !claude.UsesToken(*saved) {
		t.Fatalf("saved %+v", saved)
	}
	if err := Save(acc, got); err == nil {
		t.Fatal("saved the same id twice")
	}
}

func TestCaptureReportsAFailedRunWithoutTheToken(t *testing.T) {
	t.Setenv("GCCCODE_HOME", t.TempDir())
	s := core.DefaultSettings()
	s.ClaudeHome = t.TempDir()
	fake := filepath.Join(t.TempDir(), "claude")
	_ = os.WriteFile(fake, []byte("#!/bin/sh\necho 'OAuth error: invalid_grant'\necho 'partial sk-ant-oat01-abc'\nexit 1\n"), 0o755)
	s.ClaudeBin = fake
	st, _ := core.Load()
	acc := NewAccount(st, "", "")
	_, err := Capture(context.Background(), s, acc)
	if err == nil || !strings.Contains(err.Error(), "invalid_grant") || strings.Contains(err.Error(), "sk-ant-oat01-abc") {
		t.Fatalf("err %v", err)
	}
	Discard(acc.ID)
	if _, err := os.Stat(acc.ConfigDir); !os.IsNotExist(err) {
		t.Fatal("profile of the failed add kept")
	}
}
