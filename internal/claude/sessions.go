package claude

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nearmetips/DailiCode/internal/core"
)

// Session is one Claude Code transcript on disk.
type Session struct {
	ID   string
	Path string
	Mod  time.Time
	Cwd  string
}

// projectSessions lists the transcripts directly inside one project dir,
// newest first. Subagent transcripts live one level deeper and are skipped.
func projectSessions(dir string) []Session {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []Session
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, Session{ID: strings.TrimSuffix(e.Name(), ".jsonl"), Path: filepath.Join(dir, e.Name()), Mod: fi.ModTime()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Mod.After(out[j].Mod) })
	return out
}

// Latest returns the newest transcript written after since by a session that
// started in dir, skipping the ids skip rejects. An empty dir matches any.
func Latest(s core.Settings, dir string, since time.Time, skip func(id string) bool) (Session, bool) {
	root := filepath.Join(core.ClaudeHome(s), "projects")
	projects, err := os.ReadDir(root)
	if err != nil {
		return Session{}, false
	}
	var best Session
	for _, p := range projects {
		if !p.IsDir() {
			continue
		}
		sessions := projectSessions(filepath.Join(root, p.Name()))
		if len(sessions) == 0 || !sessions[0].Mod.After(since) || !sessions[0].Mod.After(best.Mod) {
			continue
		}
		if dir != "" && !core.SamePath(projectCwd(sessions), dir) {
			continue
		}
		for _, sess := range sessions {
			if !sess.Mod.After(since) || !sess.Mod.After(best.Mod) {
				break
			}
			if skip == nil || !skip(sess.ID) {
				best = sess
				break
			}
		}
	}
	return best, best.Path != ""
}

// Recent lists transcripts newest first, at most limit when limit > 0, only
// those started in dir unless dir is empty.
func Recent(s core.Settings, dir string, limit int) []Session {
	root := filepath.Join(core.ClaudeHome(s), "projects")
	projects, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []Session
	for _, p := range projects {
		if !p.IsDir() {
			continue
		}
		sessions := projectSessions(filepath.Join(root, p.Name()))
		if len(sessions) == 0 {
			continue
		}
		cwd := projectCwd(sessions)
		if dir != "" && !core.SamePath(cwd, dir) {
			continue
		}
		for _, sess := range sessions {
			sess.Cwd = cwd
			out = append(out, sess)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Mod.After(out[j].Mod) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// projectCwd is the directory a project's sessions were started in. Claude
// names the project dir after it lossily, so it is read from a transcript.
func projectCwd(sessions []Session) string {
	for _, sess := range sessions {
		if cwd := SessionCwd(sess.Path); cwd != "" {
			return cwd
		}
	}
	return ""
}

// SessionCwd is the directory a transcript's session was started in.
func SessionCwd(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<16), 16<<20)
	for n := 0; n < 200 && sc.Scan(); n++ {
		line := sc.Bytes()
		if !bytes.Contains(line, []byte(`"cwd"`)) {
			continue
		}
		var rec struct {
			Cwd string `json:"cwd"`
		}
		if json.Unmarshal(line, &rec) == nil && rec.Cwd != "" {
			return rec.Cwd
		}
	}
	return ""
}

// FindSession locates a transcript by its session id.
func FindSession(s core.Settings, id string) (Session, bool) {
	if id == "" || strings.ContainsAny(id, `/\`) {
		return Session{}, false
	}
	var hit Session
	root := filepath.Join(core.ClaudeHome(s), "projects")
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || hit.Path != "" {
			return nil
		}
		if d.Name() == id+".jsonl" {
			fi, _ := d.Info()
			hit = Session{ID: id, Path: p}
			if fi != nil {
				hit.Mod = fi.ModTime()
			}
			return fs.SkipAll
		}
		return nil
	})
	return hit, hit.Path != ""
}

// AccountByDir maps a CLAUDE_CONFIG_DIR back to a pool account.
func AccountByDir(st *core.Store, dir string) (*core.Account, bool) {
	if dir == "" {
		return nil, false
	}
	clean := filepath.Clean(dir)
	for i := range st.Accounts {
		a := &st.Accounts[i]
		if a.Kind == core.KindClaude && filepath.Clean(a.ConfigDir) == clean {
			return a, true
		}
	}
	return nil, false
}

// Ready reports whether a Claude account has a recorded login.
func Ready(s core.Settings, a core.Account) bool {
	if usesToken(a) {
		_, ok := ReadToken(a.ID)
		return ok && TokenDaysLeft(a, time.Now()) >= 0
	}
	_, ok := ReadIdentity(s, a.ConfigDir)
	return ok
}

// UsesToken reports whether the account signs in with a setup-token.
func UsesToken(a core.Account) bool { return usesToken(a) }

func ExecutableHint() string {
	p, err := os.Executable()
	if err != nil {
		return "gcccode"
	}
	return p
}
