package grok

import (
	"encoding/json"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nearmetips/DailiCode/internal/core"
)

// Session is one Grok thread on disk.
type Session struct {
	ID      string
	Path    string // session directory
	Cwd     string
	Mod     time.Time
	Summary string
}

// Recent lists user sessions newest first, at most limit when limit > 0,
// only those started in dir unless dir is empty.
func Recent(s core.Settings, home, dir string, limit int) []Session {
	if home == "" {
		home = core.GrokHome(s)
	}
	root := filepath.Join(home, "sessions")
	var out []Session
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		sum := filepath.Join(p, "summary.json")
		if _, err := os.Stat(sum); err != nil {
			return nil
		}
		sess, ok := readSession(p)
		if !ok {
			return nil
		}
		if dir != "" && !core.SamePath(sess.Cwd, dir) {
			return nil
		}
		out = append(out, sess)
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Mod.After(out[j].Mod) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// Latest returns the newest session written after since in dir (or any dir).
func Latest(s core.Settings, home, dir string, since time.Time) (Session, bool) {
	for _, sess := range Recent(s, home, dir, 0) {
		if sess.Mod.After(since) {
			return sess, true
		}
	}
	return Session{}, false
}

// FindSession locates a session directory by id under home.
func FindSession(s core.Settings, home, id string) (Session, bool) {
	if id == "" || strings.ContainsAny(id, `/\`) {
		return Session{}, false
	}
	if home == "" {
		home = core.GrokHome(s)
	}
	var hit Session
	root := filepath.Join(home, "sessions")
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() || hit.Path != "" {
			return nil
		}
		if d.Name() == id {
			if sess, ok := readSession(p); ok {
				hit = sess
				return fs.SkipAll
			}
		}
		return nil
	})
	return hit, hit.Path != ""
}

func readSession(dir string) (Session, bool) {
	b, err := os.ReadFile(filepath.Join(dir, "summary.json"))
	if err != nil {
		return Session{}, false
	}
	var sum struct {
		Info struct {
			ID  string `json:"id"`
			Cwd string `json:"cwd"`
		} `json:"info"`
		SessionSummary string `json:"session_summary"`
		UpdatedAt      string `json:"updated_at"`
		CreatedAt      string `json:"created_at"`
		AgentID        string `json:"agent_id"`
	}
	if json.Unmarshal(b, &sum) != nil {
		return Session{}, false
	}
	if sum.AgentID != "" {
		// Subagent / background agent sessions are not resumed by the user.
		return Session{}, false
	}
	id := sum.Info.ID
	if id == "" {
		id = filepath.Base(dir)
	}
	cwd := sum.Info.Cwd
	if cwd == "" {
		cwd = cwdFromGroup(filepath.Dir(dir))
	}
	mod := time.Time{}
	if t, err := time.Parse(time.RFC3339Nano, sum.UpdatedAt); err == nil {
		mod = t
	} else if t, err := time.Parse(time.RFC3339, sum.UpdatedAt); err == nil {
		mod = t
	} else if fi, err := os.Stat(dir); err == nil {
		mod = fi.ModTime()
	}
	return Session{ID: id, Path: dir, Cwd: cwd, Mod: mod, Summary: strings.TrimSpace(sum.SessionSummary)}, true
}

func cwdFromGroup(group string) string {
	if b, err := os.ReadFile(filepath.Join(group, ".cwd")); err == nil {
		return strings.TrimSpace(string(b))
	}
	name := filepath.Base(group)
	if u, err := url.PathUnescape(name); err == nil && strings.HasPrefix(u, "/") {
		return u
	}
	return ""
}

// ChatHistory is the transcript file inside a session directory.
func ChatHistory(sess Session) string {
	return filepath.Join(sess.Path, "chat_history.jsonl")
}

// AccountByDir maps a GROK_HOME back to a pool account.
func AccountByDir(st *core.Store, dir string) (*core.Account, bool) {
	if dir == "" {
		return nil, false
	}
	clean := filepath.Clean(dir)
	for i := range st.Accounts {
		a := &st.Accounts[i]
		if a.Kind == core.KindGrok && filepath.Clean(a.ConfigDir) == clean {
			return a, true
		}
	}
	return nil, false
}
