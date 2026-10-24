package scheduler

import (
	"errors"
	"os"
	"testing"
	"time"

	"github.com/nearmetips/DailiCode/internal/core"
)

func readyAll(core.Account) bool { return true }

func acc(id string, created time.Time) core.Account {
	return core.Account{ID: id, Kind: core.KindClaude, Label: id, ConfigDir: "/tmp/" + id, CreatedAt: created}
}

func TestSmartUsesSoonestReset(t *testing.T) {
	now := time.Date(2026, 10, 4, 10, 0, 0, 0, time.Local)
	st := &core.Store{Settings: core.DefaultSettings(), Accounts: []core.Account{acc("later", now), acc("sooner", now.Add(time.Second))}}
	st.Quota = map[string]core.Quota{
		"later":  {FiveHour: &core.Window{UsedPct: 10, ResetAt: now.Add(4 * time.Hour).Unix()}},
		"sooner": {FiveHour: &core.Window{UsedPct: 10, ResetAt: now.Add(30 * time.Minute).Unix()}},
	}
	got, _, err := Pick(st, core.KindClaude, readyAll, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.Account.ID != "sooner" {
		t.Fatalf("got %s", got.Account.ID)
	}
}

func TestNearlySpentAccountGoesLast(t *testing.T) {
	now := time.Date(2026, 10, 4, 10, 0, 0, 0, time.Local)
	st := &core.Store{Settings: core.DefaultSettings(), Accounts: []core.Account{acc("almost", now), acc("fresh", now), acc("busy", now)}}
	st.Quota = map[string]core.Quota{
		"almost": {FiveHour: &core.Window{UsedPct: 93, ResetAt: now.Add(20 * time.Minute).Unix()}},
		"fresh":  {FiveHour: &core.Window{UsedPct: 5, ResetAt: now.Add(4 * time.Hour).Unix()}},
		"busy":   {FiveHour: &core.Window{UsedPct: 30, ResetAt: now.Add(time.Hour).Unix()}},
	}
	st.State.Occupied = map[string]core.Occupancy{"busy": {PID: os.Getpid(), Since: now}}
	got, _, err := Pick(st, core.KindClaude, readyAll, nil, now)
	if err != nil || got.Account.ID != "fresh" {
		t.Fatalf("a session would run out within minutes on the account resetting soonest; got %s %v", got.Account.ID, err)
	}
	got, _, _ = Pick(st, core.KindClaude, readyAll, []string{"fresh"}, now)
	if got.Account.ID != "busy" {
		t.Fatalf("sharing an account with room beats one about to run out; got %s", got.Account.ID)
	}
	got, _, _ = Pick(st, core.KindClaude, readyAll, []string{"fresh", "busy"}, now)
	if got.Account.ID != "almost" {
		t.Fatalf("a nearly spent account is still used when nothing else is left; got %s", got.Account.ID)
	}

	st.Settings.Strategy = "pace"
	st.Quota["almost"] = core.Quota{
		FiveHour: &core.Window{UsedPct: 95, ResetAt: now.Add(time.Hour).Unix()},
		Weekly:   &core.Window{UsedPct: 1, ResetAt: now.Add(48 * time.Hour).Unix()},
	}
	st.Quota["fresh"] = core.Quota{Weekly: &core.Window{UsedPct: 50, ResetAt: now.Add(48 * time.Hour).Unix()}}
	got, _, _ = Pick(st, core.KindClaude, readyAll, []string{"busy"}, now)
	if got.Account.ID != "fresh" {
		t.Fatalf("pace must not start on an account whose 5h window is nearly spent; got %s", got.Account.ID)
	}
}

func TestPacePrefersMoreWeeklyHeadroom(t *testing.T) {
	now := time.Date(2026, 10, 4, 10, 0, 0, 0, time.Local)
	reset := now.Add(48 * time.Hour).Unix()
	st := &core.Store{Settings: core.DefaultSettings(), Accounts: []core.Account{acc("tight", now), acc("loose", now)}}
	st.Settings.Strategy = "pace"
	st.Quota = map[string]core.Quota{
		"tight": {Weekly: &core.Window{UsedPct: 90, ResetAt: reset}},
		"loose": {Weekly: &core.Window{UsedPct: 10, ResetAt: reset}},
	}
	got, _, err := Pick(st, core.KindClaude, readyAll, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.Account.ID != "loose" {
		t.Fatalf("got %s", got.Account.ID)
	}
}

func TestManualDoesNotSubstitute(t *testing.T) {
	now := time.Now()
	st := &core.Store{Settings: core.DefaultSettings(), Accounts: []core.Account{acc("a", now), acc("b", now)}}
	st.Settings.Strategy = "manual"
	st.Settings.Manual = map[string]string{core.KindClaude: "b"}
	st.State.Cooldown = map[string]int64{"b": now.Add(time.Hour).Unix()}
	_, _, err := Pick(st, core.KindClaude, readyAll, nil, now)
	if err == nil || errors.Is(err, ErrNone) {
		t.Fatalf("expected a manual-unavailable error, got %v", err)
	}
}

func TestBusyAccountIsLastResort(t *testing.T) {
	now := time.Now()
	st := &core.Store{Settings: core.DefaultSettings(), Accounts: []core.Account{acc("busy", now), acc("free", now)}}
	st.State.Occupied = map[string]core.Occupancy{"busy": {PID: 1, Since: now}}
	got, _, err := Pick(st, core.KindClaude, readyAll, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.Account.ID != "free" {
		t.Fatalf("got %s", got.Account.ID)
	}
}

func TestWaitingNamesTheFirstAccountBack(t *testing.T) {
	now := time.Date(2026, 10, 4, 10, 0, 0, 0, time.Local)
	quarantined := acc("banned", now)
	quarantined.Quarantined = true
	st := &core.Store{Settings: core.DefaultSettings(), Accounts: []core.Account{acc("weekly", now), acc("five", now), quarantined}}
	st.State.Cooldown = map[string]int64{
		"weekly": now.Add(3 * 24 * time.Hour).Unix(),
		"five":   now.Add(2*time.Hour + 5*time.Minute).Unix(),
		"banned": now.Add(time.Minute).Unix(),
	}
	_, all, err := Pick(st, core.KindClaude, readyAll, nil, now)
	if !errors.Is(err, ErrNone) {
		t.Fatalf("want ErrNone, got %v", err)
	}
	s, ok := Soonest(all)
	if !ok || s.Account.ID != "five" {
		t.Fatalf("a quarantined account never comes back on its own; got %+v", s.Account)
	}
	want := "最早恢复的是 five：12:05（约 2 小时 5 分钟后）"
	if got := Waiting(all, now); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	st.State.Cooldown = map[string]int64{}
	st.Accounts = []core.Account{quarantined}
	if got := Waiting(Evaluate(st, core.KindClaude, readyAll, now), now); got != "" {
		t.Fatalf("nothing comes back by itself, got %q", got)
	}
}

func TestFormatWait(t *testing.T) {
	now := time.Date(2026, 10, 4, 10, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		d    time.Duration
		want string
	}{
		{-time.Minute, "马上"},
		{90 * time.Second, "约 2 分钟后"},
		{3 * time.Hour, "约 3 小时后"},
		{26*time.Hour + 30*time.Minute, "约 26 小时 30 分钟后"},
		{5 * 24 * time.Hour, "约 5 天后"},
	} {
		if got := FormatWait(now.Add(c.d).Unix(), now); got != c.want {
			t.Fatalf("%v: got %q want %q", c.d, got, c.want)
		}
	}
}

func TestFormatLine(t *testing.T) {
	now := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	q := core.Quota{FiveHour: &core.Window{UsedPct: 42, ResetAt: now.Add(6 * time.Hour).Unix()}, Weekly: &core.Window{UsedPct: 18}}
	got := FormatLine("账号B", q, true, "账号C", now.UTC())
	want := "账号B · 5h 42% · 15:00 重置 · 周 18% · 下一个 账号C"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
