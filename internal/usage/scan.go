package usage

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/nearmetips/DailiCode/internal/core"
)

// file is what one transcript added up to, remembered between runs so a
// file that has not changed is never read again and one that grew is read
// from where the last run stopped.
type file struct {
	Path string `json:"-"`

	Size   int64  `json:"size"`
	Mod    int64  `json:"mod"`
	Offset int64  `json:"offset"`
	Kind   string `json:"kind"`

	Session  string `json:"session,omitempty"`
	Parent   string `json:"parent,omitempty"` // thread that spawned this one
	Sub      bool   `json:"sub,omitempty"`    // sidechain or subagent: not a session of its own
	Cwd      string `json:"cwd,omitempty"`
	Title    string `json:"title,omitempty"`
	Creator  string `json:"creator,omitempty"`  // Codex: creator_account_id
	Provider string `json:"provider,omitempty"` // Codex: model_provider
	Bound    string `json:"bound,omitempty"`    // account whose private home holds the file
	Model    string `json:"model,omitempty"`    // Codex: model of the current turn
	First    int64  `json:"first,omitempty"`    // unix seconds of the first and last call
	Last     int64  `json:"last,omitempty"`

	// Use is tokens per "YYYY-MM-DD|model" in local days.
	Use map[string]Tokens `json:"use"`

	// Parser state carried across incremental reads.
	LastID  string `json:"last_id,omitempty"` // Claude: message id counted last
	LastKey string `json:"last_key,omitempty"`
	LastUse Tokens `json:"last_use,omitempty"`
	Prev    Tokens `json:"prev,omitempty"` // Codex: cumulative total at the last event
}

func (f *file) sessionKey() string {
	if f.Parent != "" {
		return f.Parent
	}
	if f.Session != "" {
		return f.Session
	}
	return f.Path
}

func (f *file) add(t time.Time, model string, u Tokens) {
	if u.Zero() {
		return
	}
	if f.Use == nil {
		f.Use = map[string]Tokens{}
	}
	key := dayKey(t) + "|" + model
	cur := f.Use[key]
	cur.Add(u)
	f.Use[key] = cur
	ts := t.Unix()
	if f.First == 0 || ts < f.First {
		f.First = ts
	}
	if ts > f.Last {
		f.Last = ts
	}
}

func (f *file) reset() {
	f.Offset = 0
	f.Use = map[string]Tokens{}
	f.First, f.Last = 0, 0
	f.LastID, f.LastKey, f.LastUse = "", "", Tokens{}
	f.Prev = Tokens{}
}

type cache struct {
	mu     sync.Mutex
	path   string
	files  map[string]*file
	loaded bool
	dirty  bool
}

var shared = &cache{}

// open returns the process-wide cache backed by path, loading it on first
// use and reloading when the store moved (tests do that).
func open(path string) *cache {
	shared.mu.Lock()
	defer shared.mu.Unlock()
	if shared.loaded && shared.path == path {
		return shared
	}
	shared.path = path
	shared.files = map[string]*file{}
	shared.loaded = true
	shared.dirty = false
	b, err := os.ReadFile(path)
	if err != nil {
		return shared
	}
	var disk map[string]*file
	if json.Unmarshal(b, &disk) != nil {
		return shared
	}
	for p, f := range disk {
		if f == nil {
			continue
		}
		f.Path = p
		if f.Use == nil {
			f.Use = map[string]Tokens{}
		}
		shared.files[p] = f
	}
	return shared
}

// save writes the cache when something changed. Called with mu held.
func (c *cache) save() {
	if !c.dirty || c.path == "" {
		return
	}
	b, err := json.Marshal(c.files)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return
	}
	if core.WriteFileAtomic(c.path, b, 0o600) == nil {
		c.dirty = false
	}
}

// Reset forgets everything cached in this process. Tests use it to prove
// the on-disk cache carries the same answer.
func Reset() {
	shared.mu.Lock()
	defer shared.mu.Unlock()
	shared.loaded = false
	shared.files = nil
}

// source is one transcript to read.
type source struct {
	path  string
	kind  string
	bound string
	mod   time.Time
}

// scan reads every transcript modified since from and returns the summaries
// of those files. Called with mu held.
func (c *cache) scan(st *core.Store, from time.Time) []*file {
	var out []*file
	for _, s := range sources(st, from) {
		f := c.files[s.path]
		if f == nil || f.Kind != s.kind {
			f = &file{Path: s.path, Kind: s.kind, Use: map[string]Tokens{}}
			c.files[s.path] = f
		}
		f.Bound = s.bound
		changed, err := c.read(f)
		if errors.Is(err, fs.ErrNotExist) {
			delete(c.files, s.path)
			c.dirty = true
			continue
		}
		if changed {
			c.dirty = true
		}
		out = append(out, f)
	}
	return out
}

// read brings f up to date with its file. It reports whether anything
// changed.
func (c *cache) read(f *file) (bool, error) {
	fi, err := os.Stat(f.Path)
	if err != nil {
		return false, err
	}
	if fi.Size() == f.Size && fi.ModTime().UnixNano() == f.Mod {
		return false, nil
	}
	switch f.Kind {
	case core.KindClaude:
		err = readLines(f, fi, scanClaudeLine)
	case core.KindCodex:
		err = readLines(f, fi, scanCodexLine)
	case core.KindGrok:
		if f.Session == "" {
			readGrokSummary(f)
		}
		err = readLines(f, fi, scanGrokLine)
	case core.KindGemini:
		f.reset()
		err = readGemini(f)
	}
	if err != nil {
		return false, err
	}
	if f.Title == "" && !f.Sub {
		f.Title = title(f)
	}
	f.Size, f.Mod = fi.Size(), fi.ModTime().UnixNano()
	return true, nil
}

// readLines feeds every complete line after f.Offset to fn. A file that
// shrank was rewritten and is read from the start. A trailing line without
// its newline is used only when it already parses as JSON; otherwise it is
// left for the next read.
func readLines(f *file, fi os.FileInfo, fn func(f *file, line []byte)) error {
	if fi.Size() < f.Offset {
		f.reset()
	}
	fh, err := os.Open(f.Path)
	if err != nil {
		return err
	}
	defer fh.Close()
	if f.Offset > 0 {
		if _, err := fh.Seek(f.Offset, io.SeekStart); err != nil {
			f.reset()
			fh.Seek(0, io.SeekStart)
		}
	}
	r := bufio.NewReaderSize(fh, 1<<20)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 && (err == nil || json.Valid(bytes.TrimSpace(line))) {
			fn(f, bytes.TrimSpace(line))
			f.Offset += int64(len(line))
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

// sources lists the transcripts modified since from across every client.
func sources(st *core.Store, from time.Time) []source {
	var out []source
	s := st.Settings

	// Claude: one projects tree shared by every account.
	root := filepath.Join(core.ClaudeHome(s), "projects")
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(d.Name(), ".jsonl") {
			return nil
		}
		if fi, err := d.Info(); err == nil && !fi.ModTime().Before(from) {
			out = append(out, source{path: p, kind: core.KindClaude, mod: fi.ModTime()})
		}
		return nil
	})

	// Codex: one shared home; threads carry their creator.
	home := core.CodexHome(s)
	for _, dir := range []string{filepath.Join(home, "sessions"), filepath.Join(home, "archived_sessions")} {
		filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasPrefix(d.Name(), "rollout-") || !strings.HasSuffix(d.Name(), ".jsonl") {
				return nil
			}
			if fi, err := d.Info(); err == nil && !fi.ModTime().Before(from) {
				out = append(out, source{path: p, kind: core.KindCodex, mod: fi.ModTime()})
			}
			return nil
		})
	}

	// Grok and Gemini: a private home per account, plus the user's own.
	for _, h := range homes(st, core.KindGrok, core.GrokHome(s)) {
		filepath.WalkDir(filepath.Join(h.dir, "sessions"), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || d.Name() != "updates.jsonl" {
				return nil
			}
			if fi, err := d.Info(); err == nil && !fi.ModTime().Before(from) {
				out = append(out, source{path: p, kind: core.KindGrok, bound: h.account, mod: fi.ModTime()})
			}
			return nil
		})
	}
	for _, h := range homes(st, core.KindGemini, core.GeminiHome(s)) {
		filepath.WalkDir(filepath.Join(h.dir, "tmp"), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(d.Name(), ".json") {
				return nil
			}
			if filepath.Base(filepath.Dir(p)) != "chats" && !strings.HasPrefix(d.Name(), "session-") {
				return nil
			}
			if fi, err := d.Info(); err == nil && !fi.ModTime().Before(from) {
				out = append(out, source{path: p, kind: core.KindGemini, bound: h.account, mod: fi.ModTime()})
			}
			return nil
		})
	}
	return out
}

type home struct {
	dir     string
	account string
}

// homes lists the config homes of one kind: each pool account's, then the
// user's own unless an account already owns it.
func homes(st *core.Store, kind, main string) []home {
	var out []home
	seen := map[string]bool{}
	for _, a := range st.ByKind(kind) {
		if a.ConfigDir == "" {
			continue
		}
		clean := filepath.Clean(a.ConfigDir)
		if seen[clean] {
			continue
		}
		seen[clean] = true
		out = append(out, home{dir: a.ConfigDir, account: a.ID})
	}
	if main != "" && !seen[filepath.Clean(main)] {
		out = append(out, home{dir: main})
	}
	return out
}
