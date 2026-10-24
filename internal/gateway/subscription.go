package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/nearmetips/DailiCode/internal/claude"
	"github.com/nearmetips/DailiCode/internal/codex"
	"github.com/nearmetips/DailiCode/internal/core"
	"github.com/nearmetips/DailiCode/internal/scheduler"
	"github.com/nearmetips/DailiCode/internal/subauth"
)

// subCoolAuth parks an account that needs signing in again; its cooldown is
// long because only the user can fix it.
const (
	subCoolAuth = 6 * time.Hour
	subCoolBusy = 20 * time.Second
	quotaWrite  = 30 * time.Second
)

// binding is the account a session is stuck to.
type binding struct {
	account string
	at      time.Time
}

// subKey names an account in the in-memory cooldown and stats maps, apart
// from the user's provider names.
func subKey(id string) string { return "sub:" + id }

// label is the name an account is shown by.
func label(a core.Account) string {
	if a.Label != "" {
		return a.Label
	}
	return a.ID
}

// clientFor returns the HTTP client an account's requests leave through: its
// own proxy when it has one, otherwise the shared client.
func (s *Server) clientFor(acc core.Account) *http.Client {
	proxy := strings.TrimSpace(acc.Proxy)
	if proxy == "" {
		return s.client
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.clients[proxy]; ok {
		return c
	}
	tr := s.client.Transport.(*http.Transport).Clone()
	if u, err := url.Parse(proxy); err == nil && u.Host != "" {
		tr.Proxy = http.ProxyURL(u)
	}
	c := &http.Client{Transport: tr}
	s.clients[proxy] = c
	return c
}

// subProvider is the first subscription provider of a kind, or false.
func subProvider(cfg core.Gateway, kind string) (core.GatewayProvider, bool) {
	for _, p := range cfg.Providers {
		if p.Disabled || ValidateProvider(p) != nil {
			continue
		}
		if p.Subscription == kind {
			return p, true
		}
	}
	return core.GatewayProvider{}, false
}

// hasAPIFallback reports whether, after the pool is spent, an API-key upstream
// could still answer this route.
func (s *Server) hasAPIFallback(cfg core.Gateway, serves func(core.GatewayProvider) bool) bool {
	return len(servingProviders(cfg, serves)) > 0
}

// subReady reports whether a pooled account has a login the gateway can use.
func subReady(st *core.Store, a core.Account) bool {
	switch a.Kind {
	case core.KindCodex:
		_, err := os.Stat(codex.VaultPath(a.ID))
		return err == nil
	case core.KindClaude:
		return claude.Ready(st.Settings, a)
	}
	return false
}

// orderedAccounts lists the accounts a subscription request may run on, best
// first. A session already bound to a usable account leads; when bound is
// final (Claude keeps a conversation on one login), only that account is
// returned.
func (s *Server) orderedAccounts(st *core.Store, p core.GatewayProvider, bound string, boundFinal bool) ([]core.Account, []scheduler.Status) {
	allow := map[string]bool{}
	for _, id := range p.Accounts {
		allow[id] = true
	}
	ready := func(a core.Account) bool { return subReady(st, a) }
	ordered, all, err := scheduler.Order(st, p.Subscription, ready, nil, s.now())
	if err != nil {
		ordered = nil
	}
	var out []core.Account
	var boundAcc *core.Account
	for i := range ordered {
		a := ordered[i].Account
		if len(allow) > 0 && !allow[a.ID] {
			continue
		}
		if a.ID == bound {
			ac := a
			boundAcc = &ac
			continue
		}
		out = append(out, a)
	}
	if boundAcc != nil {
		if boundFinal {
			return []core.Account{*boundAcc}, all
		}
		out = append([]core.Account{*boundAcc}, out...)
	} else if boundFinal && bound != "" {
		// The bound account is not currently usable; keep the session on it
		// so the caller can report its real state rather than silently moving.
		if a, ok := st.Account(bound); ok {
			return []core.Account{*a}, all
		}
	}
	return out, all
}

// sessionBind reads the sticky binding for a session.
func (s *Server) sessionBind(st *core.Store, kind, key string) string {
	if key == "" {
		return ""
	}
	s.mu.Lock()
	b, ok := s.sticky[kind+":"+key]
	s.mu.Unlock()
	if ok {
		return b.account
	}
	return st.State.SessionOwner[key]
}

// bindSession records which account served a session, in memory and, so a
// later gcccode launch can honour it, in the store.
func (s *Server) bindSession(kind, key, account string) {
	if key == "" || account == "" {
		return
	}
	s.mu.Lock()
	s.sticky[kind+":"+key] = binding{account: account, at: s.now()}
	s.mu.Unlock()
	_, _ = core.Mutate(func(st *core.Store) error {
		st.State.SessionOwner[key] = account
		return nil
	})
}

// subResult is how one account's attempt ended.
type subResult int

const (
	subServed   subResult = iota // the agent got the answer
	subNext                      // try the next account
	subTerminal                  // stop: the error was written to the agent
)

// recordQuota writes an account's quota to the store, at most every
// quotaWrite unless it is newly limited.
func (s *Server) recordQuota(id string, q core.Quota) {
	if q.FiveHour == nil && q.Weekly == nil && !q.Limited {
		return
	}
	s.mu.Lock()
	last := s.quotaAt[id]
	if !q.Limited && s.now().Sub(last) < quotaWrite {
		s.mu.Unlock()
		return
	}
	s.quotaAt[id] = s.now()
	s.mu.Unlock()
	_, _ = core.Mutate(func(st *core.Store) error {
		q.AccountID = id
		q.UpdatedAt = s.now()
		if prev, ok := st.Quota[id]; ok && prev.UpdatedAt.After(q.UpdatedAt) {
			return nil
		}
		st.Quota[id] = q
		return nil
	})
}

// markExhausted parks an account out of quota and records when it comes back.
func (s *Server) markExhausted(id string, until int64, reason string) {
	cool := coolQuota
	if until > 0 {
		if d := time.Until(time.Unix(until, 0)); d > 0 {
			cool = d
		}
	}
	s.fail(subKey(id), cool, reason)
	_, _ = core.Mutate(func(st *core.Store) error {
		if until <= 0 {
			until = s.now().Add(coolQuota).Unix()
		}
		st.State.Cooldown[id] = until
		if q, ok := st.Quota[id]; ok {
			q.Limited = true
			st.Quota[id] = q
		}
		return nil
	})
}

// quarantine marks an account as needing a fresh sign-in.
func (s *Server) quarantine(id, reason string) {
	s.fail(subKey(id), subCoolAuth, reason)
	_, _ = core.Mutate(func(st *core.Store) error {
		if a, ok := st.Account(id); ok {
			a.Quarantined = true
			a.QuarantineReason = reason
		}
		return nil
	})
}

// awakeAccounts drops accounts whose sub key is still cooling.
func (s *Server) awakeAccounts(accs []core.Account) []core.Account {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []core.Account
	for _, a := range accs {
		if o, ok := s.down[subKey(a.ID)]; ok {
			if now.Before(o.Until) {
				continue
			}
			delete(s.down, subKey(a.ID))
		}
		out = append(out, a)
	}
	return out
}

// retryAfterAccounts is how long until the soonest parked account returns.
func (s *Server) retryAfterAccounts(accs []core.Account) time.Duration {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	var best time.Duration
	for _, a := range accs {
		if o, ok := s.down[subKey(a.ID)]; ok && o.Until.After(now) {
			if d := o.Until.Sub(now); best == 0 || d < best {
				best = d
			}
		}
	}
	return best
}

// classifyTokenErr turns a subauth error into how long to park the account.
// A dead login is quarantined; everything else is a short wait.
func (s *Server) classifyTokenErr(id string, err error) (cool time.Duration, reason string) {
	var relogin *subauth.ReloginError
	if errors.As(err, &relogin) {
		s.quarantine(id, relogin.Error())
		return subCoolAuth, relogin.Error()
	}
	var busy subauth.BusyError
	if errors.As(err, &busy) {
		return subCoolBusy, "号池会话占用中"
	}
	return coolNetwork, "取令牌失败：" + err.Error()
}

// readJSONString pulls a string field out of a decoded object.
func fieldString(fields map[string]json.RawMessage, name string) string {
	var s string
	if v, ok := fields[name]; ok {
		_ = json.Unmarshal(v, &s)
	}
	return s
}

// ctxOf is the request context for an attempt; a background context when the
// request is nil (tests).
func ctxOf(r *http.Request) context.Context {
	if r != nil {
		return r.Context()
	}
	return context.Background()
}
