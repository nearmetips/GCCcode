package launcher

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nearmetips/DailiCode/internal/agy"
	"github.com/nearmetips/DailiCode/internal/core"
)

// fakeAgy records "HOME|args" per launch, starts a conversation in HOME, and
// logs a spent quota when AGY_FAKE_QUOTA is set.
const fakeAgy = `#!/bin/sh
echo "$HOME|$*" >> "$AGY_CALLS"
cli="$HOME/.gemini/antigravity-cli"
mkdir -p "$cli/conversations" "$cli/log"
: > "$cli/conversations/new-conv.db"
if [ -n "$AGY_FAKE_QUOTA" ]; then
  printf 'E%s %s.999999      7 chat.go:1] RESOURCE_EXHAUSTED: Allocation Quota Reached\n' "$(date +%m%d)" "$(date +%H:%M:%S)" >> "$cli/log/cli-fake.log"
fi
`

// newAgyRig imports a signed-in agy home as the pool's one agy account.
func newAgyRig(t *testing.T) (home, calls string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("GCCCODE_HOME", root)
	home = filepath.Join(root, "home")
	calls = filepath.Join(root, "calls.txt")
	t.Setenv("AGY_CALLS", calls)
	os.MkdirAll(filepath.Join(home, ".gemini"), 0o700)
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"email":"me@x.com","sub":"1"}`))
	os.WriteFile(agy.TokenPath(home), []byte(`{"token":{"refresh_token":"rt"},"id_token":"h.`+payload+`.s"}`), 0o600)
	bin := filepath.Join(root, "fake-agy")
	if err := os.WriteFile(bin, []byte(fakeAgy), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := core.Mutate(func(s *core.Store) error {
		s.Settings.AgyBin = bin
		s.Settings.AgyHome = home
		s.Accounts = append(s.Accounts, core.Account{ID: "agy-1", Kind: core.KindAgy, Label: "me@x.com", ConfigDir: home, CreatedAt: time.Now()})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return home, calls
}

func launches(t *testing.T, calls string) []string {
	t.Helper()
	b, _ := os.ReadFile(calls)
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func TestAgyEnvRunsInTheAccountHome(t *testing.T) {
	s := core.DefaultSettings()
	s.AgyHome = "/real"
	acc := core.Account{ID: "agy-1", Kind: core.KindAgy, ConfigDir: "/real", Proxy: "http://127.0.0.1:7890"}
	env := agyEnv([]string{"HOME=/elsewhere", "GEMINI_API_KEY=k", "AGY_ADC_AUTH=1", "PATH=/bin"}, s, acc)
	if !hasEnv(env, "HOME=/real") || hasEnv(env, "HOME=/elsewhere") {
		t.Fatalf("HOME not the account's: %v", env)
	}
	if hasEnv(env, "GEMINI_API_KEY=k") || hasEnv(env, "AGY_ADC_AUTH=1") {
		t.Fatalf("API-key / ADC variables would bypass the account's login: %v", env)
	}
	if !hasEnv(env, "PATH=/bin") || !hasEnv(env, "HTTPS_PROXY=http://127.0.0.1:7890") {
		t.Fatalf("PATH or proxy lost: %v", env)
	}
}

func TestFakeAgyRunsAndRecordsTheConversation(t *testing.T) {
	home, calls := newAgyRig(t)
	if err := Run(Options{Kind: core.KindAgy, Stderr: &bytes.Buffer{}}); err != nil {
		t.Fatal(err)
	}
	if got := launches(t, calls); len(got) != 1 || got[0] != home+"|" {
		t.Fatalf("launches %q", got)
	}
	st, _ := core.Load()
	if st.State.SessionOwner["new-conv"] != "agy-1" {
		t.Fatalf("conversation owner %q", st.State.SessionOwner["new-conv"])
	}
	if exhausted, _ := st.Exhausted("agy-1", time.Now()); exhausted {
		t.Fatal("no quota error, yet the account was marked spent")
	}
}

func TestSpentAgyIsMarkedAndTheConversationKept(t *testing.T) {
	newAgyRig(t)
	t.Setenv("AGY_FAKE_QUOTA", "1")
	var notes bytes.Buffer
	if err := Run(Options{Kind: core.KindAgy, Stderr: &notes}); err != nil {
		t.Fatal(err)
	}
	st, _ := core.Load()
	if exhausted, _ := st.Exhausted("agy-1", time.Now()); !exhausted {
		t.Fatalf("quota error in the log was missed\n%s", notes.String())
	}
	if !strings.Contains(notes.String(), "gcccode resume new-conv") {
		t.Fatalf("notes should say how to resume later:\n%s", notes.String())
	}
}

func TestAgyResumePassesTheConversation(t *testing.T) {
	home, calls := newAgyRig(t)
	conv := filepath.Join(home, ".gemini", "antigravity-cli", "conversations")
	os.MkdirAll(conv, 0o755)
	os.WriteFile(filepath.Join(conv, "conv-x.db"), nil, 0o600)
	if err := Run(Options{Kind: core.KindAgy, Resume: "conv-x", Args: []string{"-p", "go on"}, Stderr: &bytes.Buffer{}}); err != nil {
		t.Fatal(err)
	}
	if got := launches(t, calls); len(got) != 1 || got[0] != home+"|--conversation conv-x -p go on" {
		t.Fatalf("launches %q", got)
	}
}
