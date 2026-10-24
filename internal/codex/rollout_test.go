package codex

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nearmetips/DailiCode/internal/core"
	"github.com/nearmetips/DailiCode/internal/safety"
)

func TestScanQuotaAndEncrypted(t *testing.T) {
	home := t.TempDir()
	st := &core.Store{Settings: core.DefaultSettings(), Accounts: []core.Account{{
		ID: "plus", Kind: core.KindCodex, RemoteID: "user-a:acc-a",
	}}}
	st.Settings.CodexHome = home
	day := time.Now().UTC()
	dir := filepath.Join(home, "sessions", day.Format("2006"), day.Format("01"), day.Format("02"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"type":"session_meta","payload":{"id":"sess-1","creator_account_id":"acc-a"}}
{"timestamp":"2026-10-04T02:00:00Z","type":"event_msg","payload":{"type":"token_count","rate_limits":{"primary":{"used_percent":33,"window_minutes":300,"resets_at":1791097687},"secondary":{"used_percent":7,"window_minutes":10080,"resets_at":1791641183},"plan_type":"plus","rate_limit_reached_type":null}}}
{"timestamp":"2026-10-04T02:05:00Z","type":"event_msg","payload":{"type":"error","message":"encrypted content could not be verified"}}
`
	path := filepath.Join(dir, "rollout-2026-10-04T02-00-00-sess-1.jsonl")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got := ScanQuota(st, time.Date(2026, 10, 4, 3, 0, 0, 0, time.UTC))
	q, ok := got["plus"]
	if !ok || q.FiveHour == nil || q.FiveHour.UsedPct != 33 || q.Weekly.UsedPct != 7 || q.PlanType != "plus" {
		t.Fatalf("quota = %+v", q)
	}
	since := time.Date(2026, 10, 4, 2, 4, 0, 0, time.UTC)
	if !EncryptedReplayFailed(path, since) {
		t.Fatal("expected encrypted replay failure")
	}
	if sig := ScanSignals(path, since); sig.Kind != safety.None {
		t.Fatalf("error text classified as %s", sig.Kind)
	}
}

func TestLatestSessionForPrefersResumedThenDirectory(t *testing.T) {
	home := t.TempDir()
	s := core.DefaultSettings()
	s.CodexHome = home
	work, other := t.TempDir(), t.TempDir()
	dir := filepath.Join(home, "sessions", "2026", "10", "06")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	write := func(id, cwd string, mod time.Time) {
		p := filepath.Join(dir, "rollout-2026-10-06T10-00-00-"+id+".jsonl")
		body := `{"type":"session_meta","payload":{"id":"` + id + `","cwd":"` + cwd + `"}}` + "\n"
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		os.Chtimes(p, mod, mod)
	}
	write("resumed", other, now.Add(-2*time.Minute))
	write("mine", work, now.Add(-time.Minute))
	write("parallel", other, now)
	since := now.Add(-time.Hour)

	if got, _ := LatestSessionFor(s, since, work, ""); got.ID != "mine" {
		t.Fatalf("got %q, want the thread started in the launch directory", got.ID)
	}
	if got, _ := LatestSessionFor(s, since, work, "resumed"); got.ID != "resumed" {
		t.Fatalf("got %q, want the resumed thread", got.ID)
	}
	if got, ok := LatestSessionFor(s, since, t.TempDir(), ""); ok {
		t.Fatalf("got %q from another directory", got.ID)
	}
}

func TestSubagentThreadsAreNotSessions(t *testing.T) {
	home := t.TempDir()
	s := core.DefaultSettings()
	s.CodexHome = home
	work := t.TempDir()
	dir := filepath.Join(home, "sessions", "2026", "10", "06")
	os.MkdirAll(dir, 0o700)
	now := time.Now()
	write := func(id, extra string, mod time.Time) {
		p := filepath.Join(dir, "rollout-2026-10-06T10-00-00-"+id+".jsonl")
		body := `{"type":"session_meta","payload":{"id":"` + id + `","cwd":"` + work + `","creator_account_id":"ws-1"` + extra + `}}` + "\n"
		os.WriteFile(p, []byte(body), 0o600)
		os.Chtimes(p, mod, mod)
	}
	write("main", `,"thread_source":"user"`, now.Add(-time.Minute))
	write("worker", `,"parent_thread_id":"main","thread_source":"subagent"`, now)

	if got, _ := LatestSessionFor(s, now.Add(-time.Hour), work, ""); got.ID != "main" {
		t.Fatalf("a subagent that wrote last was taken for the session: %q", got.ID)
	}
	recent := Recent(s, work, 0)
	if len(recent) != 1 || recent[0].ID != "main" {
		t.Fatalf("recent: %+v", recent)
	}
	st := &core.Store{Settings: s, Accounts: []core.Account{{ID: "gpt-1", Kind: core.KindCodex, RemoteID: "user-x:ws-1"}}}
	if got := Owner(st, recent[0]); got != "gpt-1" {
		t.Fatalf("owner %q", got)
	}
}
