package usage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nearmetips/DailiCode/internal/core"
)

var now = time.Date(2026, 10, 30, 15, 0, 0, 0, time.Local)

func stamp(d time.Duration) string { return now.Add(d).UTC().Format(time.RFC3339Nano) }

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func appendTo(t *testing.T, path, content string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
	// Make sure the mtime moves even on coarse filesystems.
	later := time.Now().Add(2 * time.Second)
	os.Chtimes(path, later, later)
}

func claudeLine(sessionID, msgID, model string, at string, in, out, cr, cw int64, sidechain bool) string {
	return fmt.Sprintf(`{"type":"assistant","timestamp":%q,"sessionId":%q,"cwd":"/work/app","isSidechain":%v,"message":{"id":%q,"model":%q,"role":"assistant","usage":{"input_tokens":%d,"output_tokens":%d,"cache_read_input_tokens":%d,"cache_creation_input_tokens":%d},"content":[{"type":"text","text":"ok"}]}}`+"\n",
		at, sessionID, sidechain, msgID, model, in, out, cr, cw)
}

func userLine(sessionID, text, at string) string {
	return fmt.Sprintf(`{"type":"user","timestamp":%q,"sessionId":%q,"cwd":"/work/app","message":{"role":"user","content":%q}}`+"\n", at, sessionID, text)
}

func codexMeta(id, creator, parent string) string {
	return fmt.Sprintf(`{"timestamp":%q,"type":"session_meta","payload":{"id":%q,"creator_account_id":%q,"cwd":"/work/svc","parent_thread_id":%q,"model_provider":"openai"}}`+"\n", stamp(-time.Hour), id, creator, parent)
}

func codexTurn(model string) string {
	return fmt.Sprintf(`{"timestamp":%q,"type":"turn_context","payload":{"model":%q,"cwd":"/work/svc"}}`+"\n", stamp(-time.Hour), model)
}

func codexCount(at string, in, cached, out int64) string {
	return fmt.Sprintf(`{"timestamp":%q,"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":%d,"cached_input_tokens":%d,"cache_write_input_tokens":0,"output_tokens":%d,"reasoning_output_tokens":0,"total_tokens":%d}},"rate_limits":{"primary":{"used_percent":10,"window_minutes":300,"resets_at":0}}}}`+"\n", at, in, cached, out, in+out)
}

// fixture builds a pool with one account of each kind and transcripts for all
// four clients, returning the store.
func fixture(t *testing.T) *core.Store {
	t.Helper()
	Reset()
	root := t.TempDir()
	t.Setenv("GCCCODE_HOME", root)
	claudeHome := filepath.Join(root, "claude-home")
	codexHome := filepath.Join(root, "codex-home")
	grokHome := filepath.Join(root, "grok-main")
	grokDir := core.GrokProfileDir("gk-1")
	geminiDir := core.GeminiProfileDir("ge-1")

	st, err := core.Mutate(func(s *core.Store) error {
		s.Settings.ClaudeHome = claudeHome
		s.Settings.CodexHome = codexHome
		s.Settings.GrokHome = grokHome
		s.Settings.GeminiHome = filepath.Join(root, "gemini-main")
		s.Accounts = []core.Account{
			{ID: "cl-1", Kind: core.KindClaude, Label: "工作号", ConfigDir: core.ClaudeProfileDir("cl-1")},
			{ID: "cl-2", Kind: core.KindClaude, Label: "备用号", ConfigDir: core.ClaudeProfileDir("cl-2")},
			{ID: "cx-1", Kind: core.KindCodex, Label: "ChatGPT 号", RemoteID: "user-1:acct-A"},
			{ID: "gk-1", Kind: core.KindGrok, Label: "Grok 号", ConfigDir: grokDir},
			{ID: "ge-1", Kind: core.KindGemini, Label: "Gemini 号", ConfigDir: geminiDir},
		}
		s.State.SessionOwner["s-claude-1"] = "cl-1"
		s.Quota["cl-1"] = core.Quota{AccountID: "cl-1", FiveHour: &core.Window{UsedPct: 40}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Claude: a session with a duplicated message, one error line, a sidechain
	// file under the session, and a second session nobody launched via gcccode.
	proj := filepath.Join(claudeHome, "projects", "-work-app")
	write(t, filepath.Join(proj, "s-claude-1.jsonl"),
		userLine("s-claude-1", "把登录页改成深色", stamp(-3*time.Hour))+
			claudeLine("s-claude-1", "msg_1", "claude-opus-5", stamp(-3*time.Hour), 100, 50, 1000, 200, false)+
			claudeLine("s-claude-1", "msg_1", "claude-opus-5", stamp(-3*time.Hour), 100, 50, 1000, 200, false)+ // same answer, second block
			`{"type":"assistant","timestamp":"`+stamp(-2*time.Hour)+`","sessionId":"s-claude-1","isApiErrorMessage":true,"message":{"id":"err","model":"<synthetic>","usage":{"input_tokens":0,"output_tokens":0}}}`+"\n"+
			claudeLine("s-claude-1", "msg_2", "claude-haiku-5", stamp(-2*time.Hour), 10, 5, 0, 0, false))
	write(t, filepath.Join(proj, "s-claude-1", "subagents", "agent-x.jsonl"),
		claudeLine("s-claude-1", "msg_sub", "claude-haiku-5", stamp(-2*time.Hour), 7, 3, 0, 0, true))
	write(t, filepath.Join(proj, "s-claude-2.jsonl"),
		userLine("s-claude-2", "写个 README", stamp(-26*time.Hour))+
			claudeLine("s-claude-2", "msg_9", "claude-opus-5", stamp(-26*time.Hour), 1, 1, 0, 0, false))

	// Codex: a thread by account A with three running totals (one repeated),
	// and a subagent thread of it.
	day := now.Format("2006/01/02")
	write(t, filepath.Join(codexHome, "sessions", day, "rollout-main.jsonl"),
		codexMeta("t-main", "acct-A", "")+
			`{"timestamp":"`+stamp(-time.Hour)+`","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"修一下 CI"}]}}`+"\n"+
			codexTurn("gpt-6-astra")+
			codexCount(stamp(-time.Hour), 1000, 800, 100)+
			codexCount(stamp(-50*time.Minute), 1000, 800, 100)+ // rate-limit refresh, nothing new
			codexCount(stamp(-40*time.Minute), 2500, 2000, 250))
	write(t, filepath.Join(codexHome, "sessions", day, "rollout-sub.jsonl"),
		codexMeta("t-sub", "acct-A", "t-main")+codexTurn("gpt-6-astra")+codexCount(stamp(-30*time.Minute), 300, 0, 30))

	// Grok: one session in the account's private home.
	sess := filepath.Join(grokDir, "sessions", "%2Fwork", "g-1")
	write(t, filepath.Join(sess, "summary.json"), `{"info":{"id":"g-1","cwd":"/work"},"session_summary":"排查内存泄漏","current_model_id":"grok-5"}`)
	write(t, filepath.Join(sess, "updates.jsonl"),
		fmt.Sprintf(`{"timestamp":%d,"method":"_x.ai/session/update","params":{"update":{"sessionUpdate":"turn_completed","usage":{"inputTokens":500,"outputTokens":40,"cachedReadTokens":300,"cacheCreationTokens":0,"modelCalls":2,"modelUsage":{"grok-5":{"inputTokens":500,"outputTokens":40,"cachedReadTokens":300,"cacheCreationTokens":0,"modelCalls":2}}}}}}`+"\n", now.Add(-20*time.Minute).Unix()))

	// Gemini: one chat in the account's private home.
	write(t, filepath.Join(geminiDir, "tmp", "hash", "chats", "session-1.json"),
		fmt.Sprintf(`{"sessionId":"gm-1","messages":[{"type":"user","timestamp":%q,"content":"翻译这段文字"},{"type":"gemini","timestamp":%q,"model":"gemini-3-pro","content":"...","tokens":{"input":120,"output":30,"cached":20,"thoughts":10,"tool":0,"total":180}}]}`, stamp(-10*time.Minute), stamp(-10*time.Minute)))

	return st
}

func account(rep Report, id string) AccountUsage {
	for _, a := range rep.Accounts {
		if a.ID == id {
			return a
		}
	}
	return AccountUsage{}
}

func model(rep Report, name string) ModelUsage {
	for _, m := range rep.Models {
		if m.Model == name {
			return m
		}
	}
	return ModelUsage{}
}

func TestReportAddsUpEveryClient(t *testing.T) {
	st := fixture(t)
	rep := Build(st, 7, now)

	// Claude: msg_1 counted once, the error line never, the sidechain added.
	cl := account(rep, "cl-1")
	want := Tokens{Input: 117, Output: 58, CacheRead: 1000, CacheWrite: 200, Calls: 3}
	if cl.Tokens != want {
		t.Fatalf("cl-1 %+v, want %+v", cl.Tokens, want)
	}
	if cl.Sessions != 1 || cl.FiveHour == nil || cl.FiveHour.UsedPct != 40 {
		t.Fatalf("cl-1 sessions %d quota %+v", cl.Sessions, cl.FiveHour)
	}
	// The session nobody launched via gcccode is unattributed, not guessed.
	un := account(rep, "unattributed:claude")
	if !un.Unattributed || un.Tokens.Calls != 1 || un.Sessions != 1 {
		t.Fatalf("unattributed claude %+v", un)
	}
	if account(rep, "cl-2").Tokens.Calls != 0 {
		t.Fatal("cl-2 was given someone else's usage")
	}

	// Codex: running totals turned into per-call deltas, the repeat ignored,
	// the subagent thread folded into its parent and its account.
	cx := account(rep, "cx-1")
	want = Tokens{Input: 500 + 300, Output: 250 + 30, CacheRead: 2000, Calls: 3}
	if cx.Tokens != want {
		t.Fatalf("cx-1 %+v, want %+v", cx.Tokens, want)
	}
	if cx.Sessions != 1 {
		t.Fatalf("cx-1 sessions %d, want the subagent folded into its parent", cx.Sessions)
	}
	if m := model(rep, "gpt-6-astra"); m.Kind != core.KindCodex || m.Tokens.Calls != 3 {
		t.Fatalf("gpt-6-astra %+v", m)
	}

	// Grok and Gemini are bound to the account whose home holds them.
	if gk := account(rep, "gk-1"); gk.Tokens != (Tokens{Input: 200, Output: 40, CacheRead: 300, Calls: 2}) || gk.Sessions != 1 {
		t.Fatalf("gk-1 %+v", gk)
	}
	if ge := account(rep, "ge-1"); ge.Tokens != (Tokens{Input: 100, Output: 40, CacheRead: 20, Calls: 1}) {
		t.Fatalf("ge-1 %+v", ge)
	}

	if rep.Sessions != 5 {
		t.Fatalf("sessions %d, want 5", rep.Sessions)
	}
	if len(rep.Daily) != 7 || rep.Daily[6].Date != now.Format("2006-01-02") {
		t.Fatalf("daily %d days ending %s", len(rep.Daily), rep.Daily[len(rep.Daily)-1].Date)
	}
	today := rep.Daily[6]
	if today.Kinds[core.KindClaude].Calls != 3 || today.Kinds[core.KindCodex].Calls != 3 || today.Kinds[core.KindGrok].Calls != 2 || today.Kinds[core.KindGemini].Calls != 1 {
		t.Fatalf("today's kinds %+v", today.Kinds)
	}
	if rep.Accounts[0].ID != "cx-1" {
		t.Fatalf("accounts should lead with the heaviest user, got %s", rep.Accounts[0].ID)
	}

	// Recent sessions carry the first request and the account.
	var titles []string
	for _, s := range rep.Recent {
		titles = append(titles, s.Kind+":"+s.Title+":"+s.Label)
	}
	joined := strings.Join(titles, "\n")
	for _, want := range []string{"claude:把登录页改成深色:工作号", "codex:修一下 CI:ChatGPT 号", "grok:排查内存泄漏:Grok 号", "gemini:翻译这段文字:Gemini 号", "claude:写个 README:"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("recent sessions missing %q:\n%s", want, joined)
		}
	}
	if len(rep.Notes) == 0 || !strings.Contains(rep.Notes[0], "没对上号") {
		t.Fatalf("notes %v", rep.Notes)
	}
}

func TestRangeLeavesOutOlderDays(t *testing.T) {
	st := fixture(t)
	rep := Build(st, 1, now)
	// s-claude-2 ran yesterday: not in today's report, but its file still
	// counts toward the scan because it was modified recently.
	if a := account(rep, "unattributed:claude"); a.Tokens.Calls != 0 {
		t.Fatalf("yesterday's session leaked into today: %+v", a)
	}
	if rep.Sessions != 4 {
		t.Fatalf("sessions today %d, want 4", rep.Sessions)
	}
	// A file last touched before the range is never opened.
	old := filepath.Join(core.ClaudeHome(st.Settings), "projects", "-work-app", "s-claude-2.jsonl")
	long := now.Add(-40 * 24 * time.Hour)
	os.Chtimes(old, long, long)
	Reset()
	rep = Build(st, 30, now)
	if rep.Files != 6 {
		t.Fatalf("scanned %d files, want 6 with the stale one skipped", rep.Files)
	}
}

func TestGrowingTranscriptIsReadIncrementally(t *testing.T) {
	st := fixture(t)
	first := Build(st, 7, now)
	path := filepath.Join(core.ClaudeHome(st.Settings), "projects", "-work-app", "s-claude-1.jsonl")
	fi, _ := os.Stat(path)
	c := open(core.UsageCacheFile())
	if c.files[path].Offset != fi.Size() {
		t.Fatalf("offset %d, want the whole file %d", c.files[path].Offset, fi.Size())
	}
	// A partial line (still being written) waits for its newline.
	appendTo(t, path, `{"type":"assistant","timestamp":"`+stamp(-time.Minute)+`","sessionId":"s-claude-1","message":{"id":"msg_3","model":"claude-opus-5","usage":{"input_tokens":5,"output_tokens":5}}`)
	mid := Build(st, 7, now)
	if mid.Totals.Calls != first.Totals.Calls {
		t.Fatalf("a half-written line was counted: %d -> %d", first.Totals.Calls, mid.Totals.Calls)
	}
	appendTo(t, path, "}\n")
	after := Build(st, 7, now)
	if after.Totals.Calls != first.Totals.Calls+1 || after.Totals.Input != first.Totals.Input+5 {
		t.Fatalf("appended call not counted: %+v -> %+v", first.Totals, after.Totals)
	}
	if after.Totals.Output != first.Totals.Output+5 {
		t.Fatalf("output %d -> %d", first.Totals.Output, after.Totals.Output)
	}
}

func TestCacheSurvivesRestart(t *testing.T) {
	st := fixture(t)
	first := Build(st, 7, now)
	b, err := os.ReadFile(core.UsageCacheFile())
	if err != nil {
		t.Fatalf("no cache written: %v", err)
	}
	var disk map[string]json.RawMessage
	if json.Unmarshal(b, &disk) != nil || len(disk) != first.Files {
		t.Fatalf("cache holds %d files, want %d", len(disk), first.Files)
	}
	Reset()
	again := Build(st, 7, now)
	if again.Totals != first.Totals || again.Sessions != first.Sessions {
		t.Fatalf("after restart %+v / %d, want %+v / %d", again.Totals, again.Sessions, first.Totals, first.Sessions)
	}
	// A deleted transcript drops out.
	os.Remove(filepath.Join(core.ClaudeHome(st.Settings), "projects", "-work-app", "s-claude-2.jsonl"))
	gone := Build(st, 7, now)
	if gone.Files != first.Files-1 || account(gone, "unattributed:claude").Tokens.Calls != 0 {
		t.Fatalf("deleted file still counted: files %d, %+v", gone.Files, account(gone, "unattributed:claude"))
	}
}

func TestCodexCounterRestartIsNotNegative(t *testing.T) {
	st := fixture(t)
	day := now.Format("2006/01/02")
	write(t, filepath.Join(core.CodexHome(st.Settings), "sessions", day, "rollout-reset.jsonl"),
		codexMeta("t-reset", "acct-A", "")+codexTurn("gpt-6-astra")+
			codexCount(stamp(-time.Hour), 1000, 0, 100)+
			codexCount(stamp(-30*time.Minute), 400, 0, 40)) // smaller than before: a fresh counter
	rep := Build(st, 7, now)
	cx := account(rep, "cx-1")
	// 1100 + 440 from this thread on top of the fixture's 1080 (800 in, 280 out) + 2000 cached.
	if cx.Tokens.Input != 800+1400 || cx.Tokens.Output != 280+140 {
		t.Fatalf("cx-1 %+v", cx.Tokens)
	}
}

func TestForeignCodexAccountIsNamedAsSuch(t *testing.T) {
	st := fixture(t)
	day := now.Format("2006/01/02")
	// A thread created by a ChatGPT account that is not in the pool: its
	// tokens stay away from cx-1, and the row says why it could not be matched.
	write(t, filepath.Join(core.CodexHome(st.Settings), "sessions", day, "rollout-foreign.jsonl"),
		codexMeta("t-foreign", "acct-B", "")+codexTurn("gpt-6-astra")+codexCount(stamp(-time.Hour), 500, 0, 50))
	rep := Build(st, 7, now)
	if cx := account(rep, "cx-1"); cx.Tokens.Input != 800 || cx.Sessions != 1 {
		t.Fatalf("cx-1 took the foreign thread: %+v", cx)
	}
	un := account(rep, "unattributed:codex")
	if !un.Unattributed || un.Tokens.Input != 500 || un.Why != "用的 ChatGPT 号不在号池里" {
		t.Fatalf("foreign codex row %+v", un)
	}
}

func TestDaysAreClamped(t *testing.T) {
	st := fixture(t)
	if rep := Build(st, 0, now); rep.Days != 1 || len(rep.Daily) != 1 {
		t.Fatalf("days 0 -> %d/%d", rep.Days, len(rep.Daily))
	}
	if rep := Build(st, 9999, now); rep.Days != MaxDays {
		t.Fatalf("days 9999 -> %d", rep.Days)
	}
}
