package quota

import (
	"testing"
	"time"

	"github.com/nearmetips/DailiCode/internal/core"
	"github.com/nearmetips/DailiCode/internal/safety"
)

func TestLimitCoolsDownUntilTheSpentWindowRenews(t *testing.T) {
	now := time.Now()
	soon := now.Add(2 * time.Hour).Unix()
	later := now.Add(4 * 24 * time.Hour).Unix()
	if got := spentReset(core.Quota{FiveHour: &core.Window{UsedPct: 99, ResetAt: soon}, Weekly: &core.Window{UsedPct: 40, ResetAt: later}}, 98, now); got != soon {
		t.Fatalf("a spent five-hour window was held until the weekly reset: %v", time.Unix(got, 0))
	}
	if got := spentReset(core.Quota{FiveHour: &core.Window{UsedPct: 99, ResetAt: soon}, Weekly: &core.Window{UsedPct: 100, ResetAt: later}}, 98, now); got != later {
		t.Fatal("with both windows spent the account waits for the later one")
	}
	if got := spentReset(core.Quota{FiveHour: &core.Window{UsedPct: 70, ResetAt: soon}, Weekly: &core.Window{UsedPct: 90, ResetAt: later}}, 98, now); got != later {
		t.Fatal("with stale numbers the fullest window is the best guess")
	}

	st := &core.Store{Settings: core.DefaultSettings(), Accounts: []core.Account{{ID: "a", Kind: core.KindClaude}}, Quota: map[string]core.Quota{}}
	st.State.Cooldown = map[string]int64{}
	said := now.Add(20 * time.Minute).Unix()
	ApplySignal(st, "a", safety.Signal{Kind: safety.Limit, Until: said})
	if st.State.Cooldown["a"] != said {
		t.Fatalf("the time the message named was ignored: %v", time.Unix(st.State.Cooldown["a"], 0))
	}
	ApplySignal(st, "a", safety.Signal{Kind: safety.Limit})
	if d := time.Until(time.Unix(st.State.Cooldown["a"], 0)); d < 4*time.Hour || d > 5*time.Hour+time.Minute {
		t.Fatalf("with nothing known the fallback is five hours, got %v", d)
	}
}
