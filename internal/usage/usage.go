// Package usage adds up what the official clients record on this machine
// about every model call: Claude Code transcripts, Codex rollouts, Grok
// session updates and Gemini CLI chats all carry token counts. The report
// folds them into per-day, per-account, per-model and per-session totals.
// Nothing is sent anywhere; the files are only read.
package usage

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nearmetips/DailiCode/internal/codex"
	"github.com/nearmetips/DailiCode/internal/core"
	"github.com/nearmetips/DailiCode/internal/handoff"
)

// Tokens is a token count, split the way the clients report it. Input
// excludes what was served from the prompt cache; CacheRead and CacheWrite
// hold that part. Calls counts model requests.
type Tokens struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cache_read"`
	CacheWrite int64 `json:"cache_write"`
	Calls      int64 `json:"calls"`
}

// Add folds o into t.
func (t *Tokens) Add(o Tokens) {
	t.Input += o.Input
	t.Output += o.Output
	t.CacheRead += o.CacheRead
	t.CacheWrite += o.CacheWrite
	t.Calls += o.Calls
}

// Sub takes o back out of t.
func (t *Tokens) Sub(o Tokens) {
	t.Input -= o.Input
	t.Output -= o.Output
	t.CacheRead -= o.CacheRead
	t.CacheWrite -= o.CacheWrite
	t.Calls -= o.Calls
}

// Total is every token that went through the model, cache included.
func (t Tokens) Total() int64 { return t.Input + t.Output + t.CacheRead + t.CacheWrite }

// Zero reports a count with nothing in it.
func (t Tokens) Zero() bool { return t.Total() == 0 && t.Calls == 0 }

// Report is the usage page's data for one range of local days.
type Report struct {
	Days     int            `json:"days"`
	From     string         `json:"from"`
	To       string         `json:"to"`
	Totals   Tokens         `json:"totals"`
	Sessions int            `json:"sessions"`
	Daily    []Day          `json:"daily"`
	Accounts []AccountUsage `json:"accounts"`
	Models   []ModelUsage   `json:"models"`
	Recent   []SessionUsage `json:"recent"`
	Notes    []string       `json:"notes"`
	Files    int            `json:"files"`
}

// Day is one local day's usage, split by client kind.
type Day struct {
	Date   string            `json:"date"`
	Kinds  map[string]Tokens `json:"kinds"`
	Tokens Tokens            `json:"tokens"`
}

// AccountUsage is one pool account's share. Unattributed rows stand for
// sessions of a kind that no account could be matched to.
type AccountUsage struct {
	ID           string       `json:"id"`
	Kind         string       `json:"kind"`
	Label        string       `json:"label"`
	Email        string       `json:"email,omitempty"`
	Plan         string       `json:"plan,omitempty"`
	Tokens       Tokens       `json:"tokens"`
	Sessions     int          `json:"sessions"`
	LastAt       int64        `json:"last_at,omitempty"`
	FiveHour     *core.Window `json:"five_hour,omitempty"`
	Weekly       *core.Window `json:"weekly,omitempty"`
	Unattributed bool         `json:"unattributed,omitempty"`
	// Why says, for an unattributed row, what kept the sessions from being matched.
	Why string `json:"why,omitempty"`
}

// ModelUsage is one model's share.
type ModelUsage struct {
	Model  string `json:"model"`
	Kind   string `json:"kind"`
	Tokens Tokens `json:"tokens"`
}

// SessionUsage is one session the user ran, with what it cost in tokens.
type SessionUsage struct {
	Kind    string `json:"kind"`
	ID      string `json:"id"`
	Account string `json:"account,omitempty"`
	Label   string `json:"label,omitempty"`
	Cwd     string `json:"cwd,omitempty"`
	Title   string `json:"title,omitempty"`
	At      int64  `json:"at"`
	Tokens  Tokens `json:"tokens"`
}

// MaxDays bounds the range the page may ask for.
const MaxDays = 365

// recentLimit is how many sessions the report lists.
const recentLimit = 12

// Build reads every transcript touched in the last days local days and
// returns the report. days is clamped to 1..MaxDays.
func Build(st *core.Store, days int, now time.Time) Report {
	if days < 1 {
		days = 1
	}
	if days > MaxDays {
		days = MaxDays
	}
	now = now.Local()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	from := today.AddDate(0, 0, -(days - 1))
	fromKey, toKey := dayKey(from), dayKey(today)

	c := open(core.UsageCacheFile())
	c.mu.Lock()
	defer c.mu.Unlock()

	files := c.scan(st, from)
	rep := Report{Days: days, From: fromKey, To: toKey, Files: len(files), Notes: []string{}}

	daily := map[string]*Day{}
	for d := from; !d.After(today); d = d.AddDate(0, 0, 1) {
		daily[dayKey(d)] = &Day{Date: dayKey(d), Kinds: map[string]Tokens{}}
	}
	type sessKey struct{ kind, id string }
	sessions := map[sessKey]*SessionUsage{}
	sessionFile := map[sessKey]*file{}
	accounts := map[string]*AccountUsage{}
	for _, a := range st.Accounts {
		label := a.Label
		if label == "" {
			label = a.ID
		}
		row := &AccountUsage{ID: a.ID, Kind: a.Kind, Label: label, Email: a.Email, Plan: a.Plan}
		if q, ok := st.Quota[a.ID]; ok {
			row.FiveHour, row.Weekly = q.FiveHour, q.Weekly
			if row.Plan == "" {
				row.Plan = q.PlanType
			}
		}
		accounts[a.ID] = row
	}
	accountSessions := map[string]map[sessKey]bool{}
	models := map[string]*ModelUsage{}
	unattributed := map[string]int64{}

	for _, f := range files {
		owner := owner(st, f)
		sk := sessKey{f.Kind, f.sessionKey()}
		for key, u := range f.Use {
			day, model, _ := strings.Cut(key, "|")
			if day < fromKey || day > toKey {
				continue
			}
			d := daily[day]
			if d == nil {
				continue
			}
			kt := d.Kinds[f.Kind]
			kt.Add(u)
			d.Kinds[f.Kind] = kt
			d.Tokens.Add(u)
			rep.Totals.Add(u)

			mk := f.Kind + "|" + model
			if models[mk] == nil {
				models[mk] = &ModelUsage{Model: model, Kind: f.Kind}
			}
			models[mk].Tokens.Add(u)

			if owner == "" {
				unattributed[f.Kind] += u.Calls
			}
			rowID := owner
			if rowID == "" {
				rowID = "unattributed:" + f.Kind
				if accounts[rowID] == nil {
					accounts[rowID] = &AccountUsage{ID: rowID, Kind: f.Kind, Label: "没对上号的会话", Unattributed: true}
				}
				if f.Kind == core.KindCodex && f.Creator != "" {
					// The file names its ChatGPT account; it just isn't one of ours.
					accounts[rowID].Why = "用的 ChatGPT 号不在号池里"
				}
			}
			row := accounts[rowID]
			row.Tokens.Add(u)
			if accountSessions[rowID] == nil {
				accountSessions[rowID] = map[sessKey]bool{}
			}
			accountSessions[rowID][sk] = true

			s := sessions[sk]
			if s == nil {
				s = &SessionUsage{Kind: f.Kind, ID: sk.id, Account: owner}
				sessions[sk] = s
			}
			s.Tokens.Add(u)
		}
		if s := sessions[sk]; s != nil {
			if f.Last > s.At {
				s.At = f.Last
			}
			if s.Cwd == "" {
				s.Cwd = f.Cwd
			}
			// The main transcript names the session; sidechains and subagent
			// threads only add tokens to it.
			if !f.Sub && (sessionFile[sk] == nil || f.Last > sessionFile[sk].Last) {
				sessionFile[sk] = f
			}
			if row := accounts[ownerRow(owner, f.Kind)]; row != nil && f.Last > row.LastAt {
				row.LastAt = f.Last
			}
		}
	}

	for id, set := range accountSessions {
		accounts[id].Sessions = len(set)
	}
	rep.Sessions = len(sessions)

	for d := from; !d.After(today); d = d.AddDate(0, 0, 1) {
		rep.Daily = append(rep.Daily, *daily[dayKey(d)])
	}

	for _, a := range st.Accounts {
		rep.Accounts = append(rep.Accounts, *accounts[a.ID])
	}
	var extra []AccountUsage
	for id, row := range accounts {
		if strings.HasPrefix(id, "unattributed:") {
			extra = append(extra, *row)
		}
	}
	sort.Slice(extra, func(i, j int) bool { return extra[i].Kind < extra[j].Kind })
	rep.Accounts = append(rep.Accounts, extra...)
	sort.SliceStable(rep.Accounts, func(i, j int) bool {
		return rep.Accounts[i].Tokens.Total() > rep.Accounts[j].Tokens.Total()
	})
	if rep.Accounts == nil {
		rep.Accounts = []AccountUsage{}
	}

	for _, m := range models {
		rep.Models = append(rep.Models, *m)
	}
	sort.Slice(rep.Models, func(i, j int) bool {
		if rep.Models[i].Tokens.Total() != rep.Models[j].Tokens.Total() {
			return rep.Models[i].Tokens.Total() > rep.Models[j].Tokens.Total()
		}
		return rep.Models[i].Model < rep.Models[j].Model
	})
	if rep.Models == nil {
		rep.Models = []ModelUsage{}
	}

	var recent []*SessionUsage
	for sk, s := range sessions {
		if sessionFile[sk] == nil {
			continue
		}
		recent = append(recent, s)
	}
	sort.Slice(recent, func(i, j int) bool { return recent[i].At > recent[j].At })
	if len(recent) > recentLimit {
		recent = recent[:recentLimit]
	}
	for _, s := range recent {
		f := sessionFile[sessKey{s.Kind, s.ID}]
		if f.Title == "" {
			f.Title = title(f)
			c.dirty = true
		}
		s.Title = f.Title
		s.Cwd = tilde(s.Cwd)
		if a, ok := st.Account(s.Account); ok {
			s.Label = a.Label
			if s.Label == "" {
				s.Label = a.ID
			}
		}
		rep.Recent = append(rep.Recent, *s)
	}
	if rep.Recent == nil {
		rep.Recent = []SessionUsage{}
	}

	rep.Notes = notes(st, unattributed)
	c.save()
	return rep
}

func ownerRow(owner, kind string) string {
	if owner == "" {
		return "unattributed:" + kind
	}
	return owner
}

// owner is the pool account a transcript ran on, or "" when nothing on
// disk ties it to one.
func owner(st *core.Store, f *file) string {
	if f.Bound != "" {
		return f.Bound
	}
	if o := st.State.SessionOwner[f.Session]; o != "" {
		return o
	}
	if f.Parent != "" {
		if o := st.State.SessionOwner[f.Parent]; o != "" {
			return o
		}
	}
	switch f.Kind {
	case core.KindClaude:
		// Sessions not started through gcccode ran on the login of the
		// user's own Claude home; that is an account only when it is in the pool.
		home := filepath.Clean(core.ClaudeHome(st.Settings))
		for _, a := range st.Accounts {
			if a.Kind == core.KindClaude && a.ConfigDir != "" && filepath.Clean(a.ConfigDir) == home {
				return a.ID
			}
		}
	case core.KindCodex:
		if f.Creator != "" {
			return codex.Owner(st, codex.Session{Creator: f.Creator})
		}
	}
	return ""
}

func notes(st *core.Store, unattributed map[string]int64) []string {
	out := []string{}
	var kinds []string
	for k, n := range unattributed {
		if n > 0 {
			kinds = append(kinds, k)
		}
	}
	sort.Strings(kinds)
	if len(kinds) > 0 {
		out = append(out, "有些会话不是从 GCC Code 启动的，对不上用的是哪个号，单独列为「没对上号的会话」。")
	}
	if len(st.ByKind(core.KindAgy)) > 0 {
		out = append(out, "Antigravity 不在本机记录 token 用量，这里统计不到它。")
	}
	return out
}

// title is a session's first request, read from the transcript that names it.
func title(f *file) string {
	if f.Kind == "gemini" {
		return f.Title
	}
	p := f.Path
	if f.Kind == core.KindGrok {
		p = filepath.Dir(p)
	}
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return handoff.Title(p)
}

// dayKey names a local day.
func dayKey(t time.Time) string { return t.Local().Format("2006-01-02") }

// tilde shortens a path under the home directory for display.
func tilde(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || p == "" {
		return p
	}
	if p == home {
		return "~"
	}
	if rest, ok := strings.CutPrefix(p, home+string(filepath.Separator)); ok {
		return "~" + string(filepath.Separator) + rest
	}
	return p
}
