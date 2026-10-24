package gemini

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nearmetips/DailiCode/internal/core"
)

// Session is one Gemini CLI chat on disk.
type Session struct {
	ID    string
	Path  string
	Cwd   string
	Mod   time.Time
	Title string
}

// Recent lists chats newest first. Gemini stores them under tmp/<hash>/chats
// or as *.json under history/; layouts vary by CLI version.
func Recent(s core.Settings, home, dir string, limit int) []Session {
	if home == "" {
		home = core.GeminiHome(s)
	}
	var out []Session
	seen := map[string]bool{}
	add := func(sess Session) {
		if sess.ID == "" || seen[sess.ID] {
			return
		}
		if dir != "" && sess.Cwd != "" && !core.SamePath(sess.Cwd, dir) {
			return
		}
		if dir != "" && sess.Cwd == "" {
			return
		}
		seen[sess.ID] = true
		out = append(out, sess)
	}
	_ = filepath.WalkDir(filepath.Join(home, "tmp"), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".json") && !strings.HasSuffix(name, ".jsonl") {
			return nil
		}
		if sess, ok := readChatFile(p); ok {
			add(sess)
		}
		return nil
	})
	_ = filepath.WalkDir(filepath.Join(home, "history"), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".json") && !strings.HasSuffix(d.Name(), ".jsonl") {
			return nil
		}
		if sess, ok := readChatFile(p); ok {
			add(sess)
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Mod.After(out[j].Mod) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// Latest returns the newest chat written after since in dir (or any).
func Latest(s core.Settings, home, dir string, since time.Time) (Session, bool) {
	for _, sess := range Recent(s, home, dir, 0) {
		if sess.Mod.After(since) {
			return sess, true
		}
	}
	return Session{}, false
}

// FindSession locates a chat by id under home.
func FindSession(s core.Settings, home, id string) (Session, bool) {
	if id == "" || strings.ContainsAny(id, `/\`) {
		return Session{}, false
	}
	if home == "" {
		home = core.GeminiHome(s)
	}
	for _, sess := range Recent(s, home, "", 0) {
		if sess.ID == id {
			return sess, true
		}
	}
	return Session{}, false
}

func readChatFile(path string) (Session, bool) {
	fi, err := os.Stat(path)
	if err != nil {
		return Session{}, false
	}
	b, err := os.ReadFile(path)
	if err != nil || len(b) == 0 {
		return Session{}, false
	}
	id := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	sess := Session{ID: id, Path: path, Mod: fi.ModTime()}
	var meta struct {
		SessionID   string `json:"sessionId"`
		ID          string `json:"id"`
		Cwd         string `json:"cwd"`
		WorkingDir  string `json:"working_directory"`
		Title       string `json:"title"`
		Description string `json:"description"`
		Messages    []struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal(b, &meta) == nil {
		if meta.SessionID != "" {
			sess.ID = meta.SessionID
		} else if meta.ID != "" {
			sess.ID = meta.ID
		}
		sess.Cwd = meta.Cwd
		if sess.Cwd == "" {
			sess.Cwd = meta.WorkingDir
		}
		sess.Title = strings.TrimSpace(meta.Title)
		if sess.Title == "" {
			sess.Title = strings.TrimSpace(meta.Description)
		}
		if sess.Title == "" {
			for _, m := range meta.Messages {
				if m.Role == "user" {
					sess.Title = oneLine(contentString(m.Content), 60)
					break
				}
			}
		}
	} else {
		// jsonl: first user line
		for _, line := range strings.Split(string(b), "\n") {
			if line == "" {
				continue
			}
			var rec struct {
				Type    string `json:"type"`
				Role    string `json:"role"`
				Content any    `json:"content"`
				Cwd     string `json:"cwd"`
			}
			if json.Unmarshal([]byte(line), &rec) != nil {
				continue
			}
			if sess.Cwd == "" && rec.Cwd != "" {
				sess.Cwd = rec.Cwd
			}
			if (rec.Type == "user" || rec.Role == "user") && sess.Title == "" {
				sess.Title = oneLine(contentString(rec.Content), 60)
			}
		}
	}
	return sess, true
}

func contentString(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case []any:
		var b strings.Builder
		for _, p := range t {
			m, _ := p.(map[string]any)
			if text, _ := m["text"].(string); text != "" {
				if b.Len() > 0 {
					b.WriteByte('\n')
				}
				b.WriteString(text)
			}
		}
		return strings.TrimSpace(b.String())
	default:
		return ""
	}
}

func oneLine(s string, limit int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit]) + "…"
}
