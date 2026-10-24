// Package quota folds official-client usage reports into the store.
package quota

import (
	"time"

	"github.com/nearmetips/DailiCode/internal/codex"
	"github.com/nearmetips/DailiCode/internal/core"
	"github.com/nearmetips/DailiCode/internal/notify"
	"github.com/nearmetips/DailiCode/internal/safety"
)

// RefreshCodex merges rate limits parsed from Codex rollout files.
func RefreshCodex(st *core.Store, now time.Time) {
	for id, q := range codex.ScanQuota(st, now) {
		prev, ok := st.Quota[id]
		if ok && !q.UpdatedAt.After(prev.UpdatedAt) {
			continue
		}
		st.Quota[id] = q
		if q.PlanType != "" {
			if a, ok := st.Account(id); ok && a.Plan == "" {
				a.Plan = q.PlanType
			}
		}
	}
}

// spentReset is when the window that ran out renews: the latest reset among
// windows at the limit, or else the fullest window's. A weekly reset days
// away must not hold an account whose five-hour window is what ran out.
func spentReset(q core.Quota, limit float64, now time.Time) int64 {
	var until, fullest int64
	top := -1.0
	for _, w := range []*core.Window{q.FiveHour, q.Weekly} {
		if w == nil || w.ResetAt <= now.Unix() {
			continue
		}
		if w.UsedPct >= limit && w.ResetAt > until {
			until = w.ResetAt
		}
		if w.UsedPct > top {
			top, fullest = w.UsedPct, w.ResetAt
		}
	}
	if until > 0 {
		return until
	}
	return fullest
}

// ApplySignal turns a client error into a cooldown or a quarantine.
func ApplySignal(st *core.Store, id string, sig safety.Signal) {
	if sig.Kind == safety.None {
		return
	}
	a, ok := st.Account(id)
	if !ok {
		return
	}
	switch sig.Kind {
	case safety.Banned, safety.NeedsLogin:
		a.Quarantined = true
		a.QuarantineReason = sig.Kind.String() + "：" + sig.Reason
		notify.Send("GCC Code", a.Label+" "+a.QuarantineReason)
	case safety.Limit:
		now := time.Now()
		until := sig.Until
		if until <= now.Unix() {
			until = spentReset(st.Quota[id], st.Settings.LimitPct, now)
		}
		if until == 0 {
			until = now.Add(5 * time.Hour).Unix()
		}
		st.State.Cooldown[id] = until
		if q, ok := st.Quota[id]; ok {
			q.Limited = true
			st.Quota[id] = q
		}
	}
}

// MaybeWarn notifies once per reset window when usage crosses the threshold.
func MaybeWarn(st *core.Store, id string, q core.Quota, now time.Time) {
	a, ok := st.Account(id)
	if !ok {
		return
	}
	var worst float64
	var mark int64
	for _, w := range []*core.Window{q.FiveHour, q.Weekly} {
		if w == nil {
			continue
		}
		if w.UsedPct > worst {
			worst = w.UsedPct
			mark = w.ResetAt
		}
	}
	if worst < st.Settings.WarnPct {
		return
	}
	if mark == 0 {
		mark = now.Unix() / 3600
	}
	if st.State.Notified[id] == mark {
		return
	}
	st.State.Notified[id] = mark
	name := a.Label
	if name == "" {
		name = a.ID
	}
	notify.Send("GCC Code 额度", name+" 已用到接近限额")
}
