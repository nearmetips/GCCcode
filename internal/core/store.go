package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func DefaultSettings() Settings {
	return Settings{
		Strategy:     "smart",
		Manual:       map[string]string{},
		LimitPct:     98,
		WarnPct:      80,
		AutoContinue: false,
		WebAddr:      "127.0.0.1:4725",
		Upsell:       true,
		Relay: Relay{
			Name:   "gcccode",
			BuyURL: "https://gcccode.com/",
		},
	}
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return nil
	}
	return json.Unmarshal(b, v)
}

// WriteFileAtomic writes via a temp file in the same dir and renames it into
// place, so a crash never leaves a half-written file.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := ensureDir(dir); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, perm); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(path, append(b, '\n'), 0o600)
}

// Lock is a cross-process lock built on an O_EXCL lock file. Locks older
// than staleAfter are assumed abandoned by a crashed process.
type Lock struct{ path string }

func Acquire(name string) (*Lock, error) { return AcquireTimeout(name, 5*time.Second) }

func AcquireTimeout(name string, wait time.Duration) (*Lock, error) {
	if err := ensureDir(Root()); err != nil {
		return nil, err
	}
	p := filepath.Join(Root(), "."+name+".lock")
	const staleAfter = 15 * time.Second
	deadline := time.Now().Add(wait)
	for {
		f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			fmt.Fprintf(f, "%d", os.Getpid())
			f.Close()
			return &Lock{path: p}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if fi, serr := os.Stat(p); serr == nil && time.Since(fi.ModTime()) > staleAfter {
			os.Remove(p)
			continue
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("另一个 gcccode 进程正在写入配置（%s）", p)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func (l *Lock) Release() {
	if l != nil {
		os.Remove(l.path)
	}
}

// Store bundles everything GCC Code persists.
type Store struct {
	Settings Settings
	Accounts []Account
	Quota    map[string]Quota
	State    State
}

// Load reads all files; missing files yield defaults.
func Load() (*Store, error) {
	s := &Store{Settings: DefaultSettings(), Quota: map[string]Quota{}}
	if err := readJSON(SettingsFile(), &s.Settings); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("读取 %s: %w", SettingsFile(), err)
	}
	if s.Settings.Manual == nil {
		s.Settings.Manual = map[string]string{}
	}
	if s.Settings.LimitPct <= 0 {
		s.Settings.LimitPct = 98
	}
	if s.Settings.WebAddr == "" {
		s.Settings.WebAddr = "127.0.0.1:4725"
	}
	var acc struct {
		Accounts []Account `json:"accounts"`
	}
	if err := readJSON(AccountsFile(), &acc); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("读取 %s: %w", AccountsFile(), err)
	}
	s.Accounts = acc.Accounts
	if err := readJSON(QuotaFile(), &s.Quota); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("读取 %s: %w", QuotaFile(), err)
	}
	if s.Quota == nil {
		s.Quota = map[string]Quota{}
	}
	if err := readJSON(StateFile(), &s.State); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("读取 %s: %w", StateFile(), err)
	}
	s.State.ensure()
	return s, nil
}

func (s *Store) SaveSettings() error { return writeJSON(SettingsFile(), s.Settings) }
func (s *Store) SaveQuota() error    { return writeJSON(QuotaFile(), s.Quota) }
func (s *Store) SaveState() error    { return writeJSON(StateFile(), s.State) }

func (s *Store) SaveAccounts() error {
	sort.SliceStable(s.Accounts, func(i, j int) bool {
		if s.Accounts[i].Kind != s.Accounts[j].Kind {
			return s.Accounts[i].Kind < s.Accounts[j].Kind
		}
		return s.Accounts[i].CreatedAt.Before(s.Accounts[j].CreatedAt)
	})
	return writeJSON(AccountsFile(), struct {
		Accounts []Account `json:"accounts"`
	}{s.Accounts})
}

// Mutate loads the store under the global lock, applies fn and saves every
// file. Used by anything that may run concurrently (statusline, launcher, web).
func Mutate(fn func(s *Store) error) (*Store, error) { return MutateTimeout(5*time.Second, fn) }

func MutateTimeout(wait time.Duration, fn func(s *Store) error) (*Store, error) {
	l, err := AcquireTimeout("store", wait)
	if err != nil {
		return nil, err
	}
	defer l.Release()
	s, err := Load()
	if err != nil {
		return nil, err
	}
	if err := fn(s); err != nil {
		return s, err
	}
	for _, save := range []func() error{s.SaveSettings, s.SaveAccounts, s.SaveQuota, s.SaveState} {
		if err := save(); err != nil {
			return s, err
		}
	}
	return s, nil
}

func (s *Store) Account(id string) (*Account, bool) {
	for i := range s.Accounts {
		if s.Accounts[i].ID == id {
			return &s.Accounts[i], true
		}
	}
	return nil, false
}

func (s *Store) ByKind(kind string) []Account {
	var out []Account
	for _, a := range s.Accounts {
		if a.Kind == kind {
			out = append(out, a)
		}
	}
	return out
}

// NewID returns the first free "<kind>-N" id, or a slug of label when given.
func (s *Store) NewID(kind, label string) string {
	if label != "" {
		base := Slug(label)
		if base != "" {
			if _, taken := s.Account(base); !taken {
				return base
			}
		}
	}
	for n := 1; ; n++ {
		id := fmt.Sprintf("%s-%d", kind, n)
		if _, taken := s.Account(id); !taken {
			return id
		}
	}
}

func Slug(v string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(v) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_' || r == '.' || r == '@' || r == ' ':
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// EffectiveQuota returns the account's quota with windows whose reset time
// has passed treated as empty again.
func EffectiveQuota(q Quota, now time.Time) Quota {
	out := q
	reset := false
	if w := q.FiveHour; w != nil && w.ResetAt > 0 && now.Unix() >= w.ResetAt {
		out.FiveHour = &Window{UsedPct: 0}
		reset = true
	}
	if w := q.Weekly; w != nil && w.ResetAt > 0 && now.Unix() >= w.ResetAt {
		out.Weekly = &Window{UsedPct: 0}
		reset = true
	}
	if reset {
		out.Limited = false
	}
	return out
}

// Exhausted reports whether an account is out of quota right now, and until when.
func (s *Store) Exhausted(id string, now time.Time) (bool, int64) {
	if until, ok := s.State.Cooldown[id]; ok && until > now.Unix() {
		return true, until
	}
	q, ok := s.Quota[id]
	if !ok {
		return false, 0
	}
	q = EffectiveQuota(q, now)
	var until int64
	hit := false
	for _, w := range []*Window{q.FiveHour, q.Weekly} {
		if w != nil && w.UsedPct >= s.Settings.LimitPct {
			hit = true
			if w.ResetAt > until {
				until = w.ResetAt
			}
		}
	}
	if q.Limited {
		hit = true
	}
	return hit, until
}

// PIDAlive reports whether a process with this pid is running.
func PIDAlive(pid int) bool { return pidAlive(pid) }

func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return processAlive(pid)
}

// Busy reports whether a live session currently holds the account.
func (s *Store) Busy(id string) bool {
	o, ok := s.State.Occupied[id]
	return ok && pidAlive(o.PID)
}

// PruneOccupancy drops occupancy entries whose process has exited.
func (s *Store) PruneOccupancy() {
	for id, o := range s.State.Occupied {
		if !pidAlive(o.PID) {
			delete(s.State.Occupied, id)
		}
	}
}
