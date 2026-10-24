package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/nearmetips/DailiCode/internal/core"
	"github.com/nearmetips/DailiCode/internal/launcher"
)

func TestClaudeResumeFlagsReachThePool(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GCCCODE_HOME", root)
	home := filepath.Join(root, "claude-home")
	work := t.TempDir()
	b, _ := json.Marshal(map[string]any{"claude_home": home})
	os.WriteFile(filepath.Join(root, "settings.json"), b, 0o600)
	transcript := filepath.Join(home, "projects", "work", "sess-1.jsonl")
	os.MkdirAll(filepath.Dir(transcript), 0o755)
	os.WriteFile(transcript, []byte(`{"type":"user","cwd":"`+work+`"}`+"\n"), 0o600)
	t.Chdir(work)

	parse := func(args ...string) (launcher.Options, []string) {
		t.Helper()
		o := launcher.Options{Kind: core.KindClaude}
		rest, err := parseLaunchFlags(&o, args)
		if err != nil {
			t.Fatal(err)
		}
		return o, takeResume(&o, rest)
	}

	o, rest := parse("-c", "--account", "claude-2", "-p", "go on")
	if o.Account != "claude-2" || o.Resume != "sess-1" || !reflect.DeepEqual(rest, []string{"-p", "go on"}) {
		t.Fatalf("-c: account=%q resume=%q rest=%v", o.Account, o.Resume, rest)
	}
	o, rest = parse("--resume=sess-1")
	if o.Resume != "sess-1" || len(rest) != 0 {
		t.Fatalf("--resume=: resume=%q rest=%v", o.Resume, rest)
	}
	o, rest = parse("-r", "auth bug")
	if o.Resume != "" || !reflect.DeepEqual(rest, []string{"-r", "auth bug"}) {
		t.Fatalf("a picker search term was taken as a session: resume=%q rest=%v", o.Resume, rest)
	}
	o, rest = parse("--resume")
	if o.Resume != "" || !reflect.DeepEqual(rest, []string{"--resume"}) {
		t.Fatalf("the bare picker flag changed: resume=%q rest=%v", o.Resume, rest)
	}
}

func TestAgyConversationFlagsReachThePool(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GCCCODE_HOME", root)
	home := filepath.Join(root, "home")
	b, _ := json.Marshal(map[string]any{"agy_home": home})
	os.WriteFile(filepath.Join(root, "settings.json"), b, 0o600)
	conv := filepath.Join(home, ".gemini", "antigravity-cli", "conversations")
	os.MkdirAll(conv, 0o755)
	os.WriteFile(filepath.Join(conv, "conv-1.db"), nil, 0o600)
	core.Mutate(func(s *core.Store) error {
		s.Accounts = append(s.Accounts, core.Account{ID: "agy-1", Kind: core.KindAgy, ConfigDir: home})
		return nil
	})
	t.Chdir(t.TempDir())

	parse := func(args ...string) (launcher.Options, []string) {
		t.Helper()
		o := launcher.Options{Kind: core.KindAgy}
		rest, err := parseLaunchFlags(&o, args)
		if err != nil {
			t.Fatal(err)
		}
		return o, takeResume(&o, rest)
	}
	o, rest := parse("--conversation", "conv-1", "--account", "agy-1", "-p", "go on")
	if o.Resume != "conv-1" || o.Account != "agy-1" || !reflect.DeepEqual(rest, []string{"-p", "go on"}) {
		t.Fatalf("--conversation: resume=%q account=%q rest=%v", o.Resume, o.Account, rest)
	}
	o, rest = parse("--conversation=conv-1")
	if o.Resume != "conv-1" || len(rest) != 0 {
		t.Fatalf("--conversation=: resume=%q rest=%v", o.Resume, rest)
	}
	o, rest = parse("--conversation", "not-here")
	if o.Resume != "" || !reflect.DeepEqual(rest, []string{"--conversation", "not-here"}) {
		t.Fatalf("an unknown conversation was taken: resume=%q rest=%v", o.Resume, rest)
	}
	o, rest = parse("-c")
	if o.Resume != "" || !reflect.DeepEqual(rest, []string{"-c"}) {
		t.Fatalf("-c with nothing indexed here should stay for agy: resume=%q rest=%v", o.Resume, rest)
	}
}

func TestSessionsMergeBothClientsForThisDirectory(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GCCCODE_HOME", root)
	claudeHome := filepath.Join(root, "claude-home")
	codexHome := filepath.Join(root, "codex-home")
	geminiHome := filepath.Join(root, "gemini-home")
	grokHome := filepath.Join(root, "grok-home")
	work, other := t.TempDir(), t.TempDir()
	b, _ := json.Marshal(map[string]any{
		"claude_home": claudeHome, "codex_home": codexHome,
		"gemini_home": geminiHome, "grok_home": grokHome,
		"agy_home": filepath.Join(root, "agy-home"),
	})
	os.WriteFile(filepath.Join(root, "settings.json"), b, 0o600)
	now := time.Now()
	put := func(p, body string, mod time.Time) {
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(body), 0o600)
		os.Chtimes(p, mod, mod)
	}
	user := func(cwd, text string) string {
		return `{"type":"user","cwd":"` + cwd + `","message":{"role":"user","content":"` + text + `"}}` + "\n"
	}
	put(filepath.Join(claudeHome, "projects", "work", "old.jsonl"), user(work, "旧任务"), now.Add(-3*time.Hour))
	put(filepath.Join(claudeHome, "projects", "work", "empty.jsonl"), `{"type":"file-history-snapshot","cwd":"`+work+`"}`+"\n", now)
	put(filepath.Join(claudeHome, "projects", "other", "elsewhere.jsonl"), user(other, "别的项目"), now.Add(-time.Minute))
	put(filepath.Join(codexHome, "sessions", "2026", "10", "06", "rollout-2026-10-06T10-00-00-codex-1.jsonl"),
		`{"type":"session_meta","payload":{"id":"codex-1","cwd":"`+work+`"}}`+"\n"+
			`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"新任务"}]}}`+"\n",
		now.Add(-time.Hour))
	geminiProf := filepath.Join(root, "gemini", "gemini-1")
	put(filepath.Join(geminiProf, "tmp", "h", "chats", "gem-1.json"),
		`{"sessionId":"gem-1","cwd":"`+work+`","messages":[{"role":"user","content":"Gemini 任务"}]}`,
		now.Add(-30*time.Minute))
	grokSess := filepath.Join(grokHome, "sessions", "%2Fwork", "grok-1")
	put(filepath.Join(grokSess, "summary.json"),
		`{"info":{"id":"grok-1","cwd":"`+work+`"},"session_summary":"Grok 任务","updated_at":"`+now.Add(-10*time.Minute).Format(time.RFC3339Nano)+`"}`,
		now.Add(-10*time.Minute))
	core.Mutate(func(s *core.Store) error {
		s.Accounts = append(s.Accounts,
			core.Account{ID: "claude-2", Kind: core.KindClaude, Label: "工作号"},
			core.Account{ID: "gemini-1", Kind: core.KindGemini, Label: "G", ConfigDir: geminiProf},
			core.Account{ID: "grok-1", Kind: core.KindGrok, Label: "K", ConfigDir: grokHome},
		)
		s.State.SessionOwner["old"] = "claude-2"
		return nil
	})
	t.Chdir(work)

	st, _ := core.Load()
	rows := recentSessions(st, work, 0)
	var ids []string
	for _, r := range rows {
		ids = append(ids, r.Kind+":"+r.ID)
	}
	want := []string{"grok:grok-1", "gemini:gem-1", "codex:codex-1", "claude:old"}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("want %v; got %v", want, ids)
	}
	if accountName(st, rows[3].Owner) != "工作号" {
		t.Fatalf("owner %q", rows[3].Owner)
	}
	latest, err := latestHere(st)
	if err != nil || latest.ID != "grok-1" || latest.Title != "Grok 任务" {
		t.Fatalf("latest %+v %v", latest, err)
	}
}
