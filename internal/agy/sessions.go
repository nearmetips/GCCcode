package agy

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/nearmetips/DailiCode/internal/core"
)

// Session is one agy conversation.
type Session struct {
	ID    string
	Path  string // the conversation's SQLite file
	Cwd   string
	Mod   time.Time
	Title string
}

func cliDir(home string) string { return filepath.Join(home, ".gemini", "antigravity-cli") }

func conversationsDir(home string) string { return filepath.Join(cliDir(home), "conversations") }

// summary is one row of conversation_summaries.db, agy's own index of its
// conversations. Conversations from older agy versions have no row.
type summary struct {
	ID         string          `json:"conversation_id"`
	Title      string          `json:"title"`
	Preview    string          `json:"preview"`
	Modified   json.RawMessage `json:"last_modified_time"`
	Workspaces string          `json:"workspace_uris"`
	Parent     string          `json:"parent_conversation_id"`
	Depth      int             `json:"nesting_depth"`
}

// sqlite finds the sqlite3 command. agy keeps its index in SQLite with a
// write-ahead log, which only SQLite reads reliably; without the command,
// conversations are still found by id but not listed.
var sqlite = func() (string, error) { return exec.LookPath("sqlite3") }

func summaries(home string) map[string]summary {
	db := filepath.Join(cliDir(home), "conversation_summaries.db")
	if _, err := os.Stat(db); err != nil {
		return nil
	}
	bin, err := sqlite()
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "-readonly", "-json", db,
		"select conversation_id, title, preview, last_modified_time, workspace_uris, parent_conversation_id, nesting_depth from conversation_summaries").Output()
	if err != nil {
		return nil
	}
	return parseSummaries(out)
}

func parseSummaries(b []byte) map[string]summary {
	var rows []summary
	if len(bytes.TrimSpace(b)) == 0 || json.Unmarshal(b, &rows) != nil {
		return nil
	}
	m := make(map[string]summary, len(rows))
	for _, r := range rows {
		if r.ID != "" {
			m[r.ID] = r
		}
	}
	return m
}

// Recent lists the user's conversations agy has indexed, newest first, at
// most limit when limit > 0, only those started in dir unless dir is empty.
func Recent(s core.Settings, home, dir string, limit int) []Session {
	if home == "" {
		home = core.AgyHome(s)
	}
	var out []Session
	for id, sum := range summaries(home) {
		if sum.Parent != "" || sum.Depth > 0 {
			continue
		}
		sess, _ := fromFile(home, id)
		fill(&sess, sum)
		if dir != "" && !core.SamePath(sess.Cwd, dir) {
			continue
		}
		out = append(out, sess)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Mod.After(out[j].Mod) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// FindSession looks up conversation id in home.
func FindSession(s core.Settings, home, id string) (Session, bool) {
	if id == "" || strings.ContainsAny(id, `/\`) || strings.Contains(id, "..") {
		return Session{}, false
	}
	if home == "" {
		home = core.AgyHome(s)
	}
	sess, onDisk := fromFile(home, id)
	sum, indexed := summaries(home)[id]
	if !onDisk && !indexed {
		return Session{}, false
	}
	if indexed {
		fill(&sess, sum)
	}
	return sess, true
}

// Latest is the newest top-level conversation written in home after since,
// preferring one agy recorded for dir. It works from file times, so it also
// finds the conversation when sqlite3 is missing. agy also rewrites idle
// conversation files when it upgrades their storage, so a file the index
// last saw used before since is skipped.
func Latest(s core.Settings, home, dir string, since time.Time) (Session, bool) {
	if home == "" {
		home = core.AgyHome(s)
	}
	entries, err := os.ReadDir(conversationsDir(home))
	if err != nil {
		return Session{}, false
	}
	var found []Session
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".db")
		if !ok || e.IsDir() {
			continue
		}
		if sess, ok := fromFile(home, id); ok && sess.Mod.After(since) {
			found = append(found, sess)
		}
	}
	if len(found) == 0 {
		return Session{}, false
	}
	sort.Slice(found, func(i, j int) bool { return found[i].Mod.After(found[j].Mod) })
	sums := summaries(home)
	for _, sess := range found {
		if sum, ok := sums[sess.ID]; ok {
			if sum.Parent != "" || sum.Depth > 0 {
				continue
			}
			if t := parseTime(sum.Modified); !t.IsZero() && t.Before(since) {
				continue
			}
			fill(&sess, sum)
			if dir != "" && sess.Cwd != "" && !core.SamePath(sess.Cwd, dir) {
				continue
			}
		}
		return sess, true
	}
	return Session{}, false
}

func fromFile(home, id string) (Session, bool) {
	path := filepath.Join(conversationsDir(home), id+".db")
	sess := Session{ID: id, Path: path}
	fi, err := os.Stat(path)
	if err != nil {
		return sess, false
	}
	sess.Mod = fi.ModTime()
	if wal, err := os.Stat(path + "-wal"); err == nil && wal.ModTime().After(sess.Mod) {
		sess.Mod = wal.ModTime()
	}
	return sess, true
}

func fill(sess *Session, sum summary) {
	sess.Title = oneLine(sum.Title, 60)
	if sess.Title == "" {
		sess.Title = oneLine(sum.Preview, 60)
	}
	sess.Cwd = firstWorkspace(sum.Workspaces)
	if t := parseTime(sum.Modified); !t.IsZero() {
		sess.Mod = t
	}
}

func parseTime(raw json.RawMessage) time.Time {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		for _, layout := range []string{
			"2006-01-02 15:04:05.999999999Z07:00", time.RFC3339Nano,
			"2006-01-02 15:04:05.999999999", "2006-01-02 15:04:05",
		} {
			if t, err := time.Parse(layout, s); err == nil {
				return t
			}
		}
		return time.Time{}
	}
	var n float64
	if json.Unmarshal(raw, &n) == nil && n > 0 {
		if n > 1e12 {
			return time.UnixMilli(int64(n))
		}
		return time.Unix(int64(n), 0)
	}
	return time.Time{}
}

// firstWorkspace is the first folder in workspace_uris, a JSON list or a
// comma-separated list of file:// URIs.
func firstWorkspace(raw string) string {
	raw = strings.TrimSpace(raw)
	var list []string
	if strings.HasPrefix(raw, "[") {
		_ = json.Unmarshal([]byte(raw), &list)
	} else {
		list = strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == '\n' })
	}
	for _, u := range list {
		if p := uriPath(strings.TrimSpace(u)); p != "" {
			return p
		}
	}
	return ""
}

func uriPath(u string) string {
	if !strings.HasPrefix(u, "file://") {
		if filepath.IsAbs(u) {
			return u
		}
		return ""
	}
	parsed, err := url.Parse(u)
	if err != nil || parsed.Path == "" {
		return ""
	}
	p := parsed.Path
	if runtime.GOOS == "windows" && len(p) > 2 && p[0] == '/' && p[2] == ':' {
		p = p[1:]
	}
	return filepath.FromSlash(p)
}

func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}
