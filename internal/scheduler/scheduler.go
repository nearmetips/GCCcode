// Package scheduler picks which account a new session runs on.
package scheduler

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/nearmetips/DailiCode/internal/core"
)

// Ready reports whether an account has a usable login on disk.
type Ready func(core.Account) bool

// Status is an account's standing at one moment.
type Status struct {
	Account   core.Account
	Quota     core.Quota
	HasQuota  bool
	Exhausted bool
	Until     int64
	Busy      bool
	Ready     bool
	Reason    string
}

// Usable reports whether the account can start a session now.
func (s Status) Usable() bool {
	return s.Ready && !s.Account.Disabled && !s.Account.Quarantined && !s.Exhausted
}

// Evaluate computes every account of one kind's status.
func Evaluate(st *core.Store, kind string, ready Ready, now time.Time) []Status {
	var out []Status
	for _, a := range st.ByKind(kind) {
		q, has := st.Quota[a.ID]
		if has {
			q = core.EffectiveQuota(q, now)
		}
		ex, until := st.Exhausted(a.ID, now)
		s := Status{Account: a, Quota: q, HasQuota: has, Exhausted: ex, Until: until, Busy: st.Busy(a.ID), Ready: ready(a)}
		switch {
		case a.Disabled:
			s.Reason = "已停用"
		case a.Quarantined:
			s.Reason = "已隔离：" + a.QuarantineReason
		case !s.Ready:
			s.Reason = "未登录"
		case ex && until > 0:
			s.Reason = "额度用完，" + FormatReset(until, now) + " 重置"
		case ex:
			s.Reason = "额度用完"
		case s.Busy:
			s.Reason = "使用中"
		default:
			s.Reason = "可用"
		}
		out = append(out, s)
	}
	return out
}

// ErrNone means no account of the kind can take a session.
var ErrNone = errors.New("没有可用账号")

// Pick chooses an account for a new session. Busy accounts are used only
// when every free one is unavailable, so parallel sessions spread out.
// Manual strategy never silently substitutes a different account.
func Pick(st *core.Store, kind string, ready Ready, exclude []string, now time.Time) (Status, []Status, error) {
	cands, all, err := Order(st, kind, ready, exclude, now)
	if err != nil {
		return Status{}, all, err
	}
	return cands[0], all, nil
}

// Order lists every account Pick could choose, best first; under the manual
// strategy that is only the chosen one.
func Order(st *core.Store, kind string, ready Ready, exclude []string, now time.Time) ([]Status, []Status, error) {
	all := Evaluate(st, kind, ready, now)
	skip := map[string]bool{}
	for _, id := range exclude {
		skip[id] = true
	}
	var free, busy []Status
	for _, s := range all {
		if skip[s.Account.ID] || !s.Usable() {
			continue
		}
		if s.Busy {
			busy = append(busy, s)
		} else {
			free = append(free, s)
		}
	}
	strategy := st.Settings.Strategy
	order := func(c []Status) {
		switch strategy {
		case "pace":
			sortPace(c, now)
		default:
			sortSmart(c)
		}
	}
	order(free)
	order(busy)
	cands := append(free, busy...)
	limit := st.Settings.LimitPct
	sort.SliceStable(cands, func(i, j int) bool { return !nearLimit(cands[i], limit) && nearLimit(cands[j], limit) })
	if strategy == "manual" {
		want := st.Settings.Manual[kind]
		if want == "" {
			return nil, all, fmt.Errorf("策略是 manual，但没有指定 %s 账号（gcccode use %s <id>）", kind, kind)
		}
		for _, s := range cands {
			if s.Account.ID == want {
				return []Status{s}, all, nil
			}
		}
		for _, s := range all {
			if s.Account.ID == want {
				return nil, all, fmt.Errorf("手动指定的账号 %s 当前不可用：%s", want, s.Reason)
			}
		}
		return nil, all, fmt.Errorf("手动指定的账号 %s 不存在", want)
	}
	if len(cands) == 0 {
		return nil, all, ErrNone
	}
	return cands, all, nil
}

// NextName is the account a new session would use, for the status line.
func NextName(st *core.Store, kind string, ready Ready, exclude []string, now time.Time) string {
	s, _, err := Pick(st, kind, ready, exclude, now)
	if err != nil {
		return ""
	}
	return Display(s.Account)
}

// Soonest is the account that comes back first among those only out of
// quota: not disabled, quarantined or logged out, and with a known reset.
func Soonest(all []Status) (Status, bool) {
	var best Status
	found := false
	for _, s := range all {
		if !s.Exhausted || s.Until <= 0 || !s.Ready || s.Account.Disabled || s.Account.Quarantined {
			continue
		}
		if !found || s.Until < best.Until {
			best, found = s, true
		}
	}
	return best, found
}

// Waiting says when a session can start again, for when no account can
// take one now; "" when no account will come back on its own.
func Waiting(all []Status, now time.Time) string {
	s, ok := Soonest(all)
	if !ok {
		return ""
	}
	return fmt.Sprintf("最早恢复的是 %s：%s（%s）", Display(s.Account), FormatReset(s.Until, now), FormatWait(s.Until, now))
}

// NoneUsable warns that no account in all can take a session, with when the
// first one comes back; "" while one can.
func NoneUsable(all []Status, now time.Time) string {
	if len(all) == 0 {
		return ""
	}
	for _, s := range all {
		if s.Usable() {
			return ""
		}
	}
	line := "现在没有能用的号"
	if w := Waiting(all, now); w != "" {
		line += "。" + w
	}
	return line
}

// FormatWait renders how long until a unix time, such as "约 2 小时 5 分钟后".
func FormatWait(unix int64, now time.Time) string {
	d := time.Unix(unix, 0).Sub(now)
	if d <= 0 {
		return "马上"
	}
	m := int(math.Ceil(d.Minutes()))
	switch {
	case m < 60:
		return fmt.Sprintf("约 %d 分钟后", m)
	case m < 48*60:
		if m%60 == 0 {
			return fmt.Sprintf("约 %d 小时后", m/60)
		}
		return fmt.Sprintf("约 %d 小时 %d 分钟后", m/60, m%60)
	default:
		return fmt.Sprintf("约 %.0f 天后", math.Round(d.Hours()/24))
	}
}

// Display is the name an account is shown by.
func Display(a core.Account) string {
	if a.Label != "" {
		return a.Label
	}
	return a.ID
}

// lowHeadroom is how close to the limit, in percentage points, an account
// may get before new sessions prefer any other: a session started there
// would have to switch accounts within minutes, and for Claude a switch
// means a handoff note instead of the original session.
const lowHeadroom = 10.0

func nearLimit(s Status, limit float64) bool {
	if !s.HasQuota {
		return false
	}
	for _, w := range []*core.Window{s.Quota.FiveHour, s.Quota.Weekly} {
		if w != nil && limit-w.UsedPct < lowHeadroom {
			return true
		}
	}
	return false
}

// sortSmart puts the account whose quota resets soonest first, so a window
// is not left unused when it renews.
func sortSmart(c []Status) {
	sort.SliceStable(c, func(i, j int) bool {
		ri, ui := smartKey(c[i])
		rj, uj := smartKey(c[j])
		if ri != rj {
			return ri < rj
		}
		return ui > uj
	})
}

func smartKey(s Status) (reset int64, used float64) {
	reset = math.MaxInt64
	if !s.HasQuota {
		return reset, -1
	}
	consider := func(w *core.Window) {
		if w == nil {
			return
		}
		if w.UsedPct > used {
			used = w.UsedPct
		}
		if w.ResetAt > 0 && w.UsedPct > 0 && w.ResetAt < reset {
			reset = w.ResetAt
		}
	}
	consider(s.Quota.FiveHour)
	consider(s.Quota.Weekly)
	return reset, used
}

// sortPace prefers the account with the most weekly quota left per hour
// until that window renews.
func sortPace(c []Status, now time.Time) {
	sort.SliceStable(c, func(i, j int) bool {
		return paceKey(c[i], now) > paceKey(c[j], now)
	})
}

func paceKey(s Status, now time.Time) float64 {
	w := s.Quota.Weekly
	if !s.HasQuota || w == nil || w.ResetAt <= now.Unix() {
		return -1
	}
	hours := float64(w.ResetAt-now.Unix()) / 3600
	if hours < 0.25 {
		hours = 0.25
	}
	return (100 - w.UsedPct) / hours
}

// FormatReset renders a unix reset time relative to now.
func FormatReset(unix int64, now time.Time) string {
	if unix <= 0 {
		return ""
	}
	t := time.Unix(unix, 0).In(now.Location())
	if t.Year() == now.Year() && t.YearDay() == now.YearDay() {
		return t.Format("15:04")
	}
	return t.Format("01-02 15:04")
}

// FormatLine is the one-line status Claude Code shows.
func FormatLine(name string, q core.Quota, has bool, next string, now time.Time) string {
	if name == "" {
		name = "gcccode"
	}
	if !has {
		line := name + " · 额度未知"
		if next != "" {
			line += " · 下一个 " + next
		}
		return line
	}
	line := name
	if q.FiveHour != nil {
		line += fmt.Sprintf(" · 5h %.0f%%", q.FiveHour.UsedPct)
		if q.FiveHour.ResetAt > 0 {
			line += " · " + FormatReset(q.FiveHour.ResetAt, now) + " 重置"
		}
	}
	if q.Weekly != nil {
		line += fmt.Sprintf(" · 周 %.0f%%", q.Weekly.UsedPct)
	}
	if next != "" {
		line += " · 下一个 " + next
	}
	return line
}
