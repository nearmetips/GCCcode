package codex

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nearmetips/DailiCode/internal/core"
	"github.com/nearmetips/DailiCode/internal/safety"
)

func sessionsDirs(s core.Settings) []string {
	home := core.CodexHome(s)
	return []string{filepath.Join(home, "sessions"), filepath.Join(home, "archived_sessions")}
}

type rolloutFile struct {
	path string
	mod  time.Time
}

// recentRollouts returns rollout files modified after since, newest first.
func recentRollouts(s core.Settings, since time.Time) []rolloutFile {
	var out []rolloutFile
	for _, root := range sessionsDirs(s) {
		filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() || !strings.HasPrefix(d.Name(), "rollout-") || !strings.HasSuffix(d.Name(), ".jsonl") {
				return nil
			}
			fi, err := d.Info()
			if err == nil && fi.ModTime().After(since) {
				out = append(out, rolloutFile{p, fi.ModTime()})
			}
			return nil
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].mod.After(out[j].mod) })
	return out
}

type sessionMeta struct {
	ID               string `json:"id"`
	CreatorAccountID string `json:"creator_account_id"`
	Cwd              string `json:"cwd"`
	ParentThreadID   string `json:"parent_thread_id"`
	ThreadSource     string `json:"thread_source"`
	ModelProvider    string `json:"model_provider"`
}

// viaOtherProvider reports a thread that ran on a provider other than
// OpenAI, such as the gcccode gateway: the limits it recorded belong to
// whatever account the gateway picked, not to the login in auth.json.
func (m sessionMeta) viaOtherProvider() bool {
	return m.ModelProvider != "" && m.ModelProvider != "openai"
}

// subagent reports whether the thread was spawned by another thread. Such a
// thread is not something the user resumes.
func (m sessionMeta) subagent() bool {
	return m.ParentThreadID != "" || m.ThreadSource == "subagent"
}

func readMeta(path string) sessionMeta {
	f, err := os.Open(path)
	if err != nil {
		return sessionMeta{}
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 1<<16)
	line, err := r.ReadSlice('\n')
	if err == bufio.ErrBufferFull {
		var buf bytes.Buffer
		buf.Write(line)
		for err == bufio.ErrBufferFull && buf.Len() < 8<<20 {
			line, err = r.ReadSlice('\n')
			buf.Write(line)
		}
		line = buf.Bytes()
	}
	var rec struct {
		Type    string      `json:"type"`
		Payload sessionMeta `json:"payload"`
	}
	if json.Unmarshal(line, &rec) != nil || rec.Type != "session_meta" {
		return sessionMeta{}
	}
	return rec.Payload
}

// tailLines returns the complete lines in the last n bytes of a file.
func tailLines(path string, n int64) [][]byte {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil
	}
	start := int64(0)
	if fi.Size() > n {
		start = fi.Size() - n
	}
	f.Seek(start, io.SeekStart)
	b, _ := io.ReadAll(f)
	lines := bytes.Split(b, []byte("\n"))
	if start > 0 && len(lines) > 0 {
		lines = lines[1:]
	}
	return lines
}

type rateWindow struct {
	UsedPercent   float64 `json:"used_percent"`
	WindowMinutes int     `json:"window_minutes"`
	ResetsAt      int64   `json:"resets_at"`
}

type rateLimits struct {
	Primary   *rateWindow `json:"primary"`
	Secondary *rateWindow `json:"secondary"`
	PlanType  string      `json:"plan_type"`
	Reached   any         `json:"rate_limit_reached_type"`
}

type eventLine struct {
	Timestamp time.Time `json:"timestamp"`
	Type      string    `json:"type"`
	Payload   struct {
		Type       string      `json:"type"`
		Message    string      `json:"message"`
		RateLimits *rateLimits `json:"rate_limits"`
	} `json:"payload"`
}

func toQuota(id string, at time.Time, rl *rateLimits) core.Quota {
	q := core.Quota{AccountID: id, PlanType: rl.PlanType, Source: "codex-rollout", UpdatedAt: at}
	for _, w := range []*rateWindow{rl.Primary, rl.Secondary} {
		if w == nil {
			continue
		}
		win := &core.Window{UsedPct: w.UsedPercent, ResetAt: w.ResetsAt}
		if w.WindowMinutes > 0 && w.WindowMinutes <= 600 {
			q.FiveHour = win
		} else {
			q.Weekly = win
		}
	}
	if rl.Reached != nil && rl.Reached != "" {
		q.Limited = true
	}
	return q
}

// ScanQuota reads the newest rate limits Codex recorded and attributes each
// to the account that was signed in when it was recorded.
func ScanQuota(st *core.Store, now time.Time) map[string]core.Quota {
	out := map[string]core.Quota{}
	byWorkspace := map[string]string{}
	for _, a := range st.ByKind(core.KindCodex) {
		if i := strings.LastIndexByte(a.RemoteID, ':'); i >= 0 {
			byWorkspace[a.RemoteID[i+1:]] = a.ID
		}
	}
	for _, rf := range recentRollouts(st.Settings, now.Add(-8*24*time.Hour)) {
		lines := tailLines(rf.path, 1<<20)
		var ev eventLine
		found := false
		for i := len(lines) - 1; i >= 0; i-- {
			if !bytes.Contains(lines[i], []byte(`"rate_limits"`)) {
				continue
			}
			if json.Unmarshal(lines[i], &ev) == nil && ev.Payload.RateLimits != nil {
				found = true
				break
			}
		}
		if !found {
			continue
		}
		m := readMeta(rf.path)
		if m.viaOtherProvider() {
			continue
		}
		id := ""
		if st.State.CodexCurrent != "" && !st.State.CodexSince.IsZero() && !ev.Timestamp.Before(st.State.CodexSince) {
			id = st.State.CodexCurrent
		} else if m.CreatorAccountID != "" {
			id = byWorkspace[m.CreatorAccountID]
		}
		if id == "" {
			continue
		}
		if prev, ok := out[id]; ok && !ev.Timestamp.After(prev.UpdatedAt) {
			continue
		}
		out[id] = toQuota(id, ev.Timestamp, ev.Payload.RateLimits)
	}
	return out
}

// Session is one Codex thread on disk.
type Session struct {
	ID      string
	Path    string
	Cwd     string
	Mod     time.Time
	Creator string
}

// LatestSessionFor returns the thread a launch in dir wrote after since: the
// thread it resumed when that one was written, otherwise the newest thread
// started in dir. An empty dir matches any. Subagent threads are skipped;
// they often write after the thread that spawned them.
func LatestSessionFor(s core.Settings, since time.Time, dir, resumed string) (Session, bool) {
	var hit Session
	for _, f := range recentRollouts(s, since) {
		m := readMeta(f.path)
		if m.subagent() {
			continue
		}
		id := m.ID
		if id == "" {
			id = idFromName(f.path)
		}
		if id == "" {
			continue
		}
		sess := Session{ID: id, Path: f.path, Cwd: m.Cwd, Mod: f.mod, Creator: m.CreatorAccountID}
		if resumed != "" && id == resumed {
			return sess, true
		}
		if hit.Path == "" && (dir == "" || core.SamePath(m.Cwd, dir)) {
			hit = sess
			if resumed == "" {
				break
			}
		}
	}
	return hit, hit.Path != ""
}

// Recent lists the user's threads newest first, at most limit when limit > 0,
// only those started in dir unless dir is empty.
func Recent(s core.Settings, dir string, limit int) []Session {
	var out []Session
	for _, f := range recentRollouts(s, time.Time{}) {
		m := readMeta(f.path)
		if m.subagent() || dir != "" && !core.SamePath(m.Cwd, dir) {
			continue
		}
		id := m.ID
		if id == "" {
			id = idFromName(f.path)
		}
		if id == "" {
			continue
		}
		out = append(out, Session{ID: id, Path: f.path, Cwd: m.Cwd, Mod: f.mod, Creator: m.CreatorAccountID})
		if limit > 0 && len(out) == limit {
			break
		}
	}
	return out
}

// Owner is the pool account a thread was started on, or "".
func Owner(st *core.Store, sess Session) string {
	if sess.Creator == "" {
		return ""
	}
	for _, a := range st.ByKind(core.KindCodex) {
		if i := strings.LastIndexByte(a.RemoteID, ':'); i >= 0 && a.RemoteID[i+1:] == sess.Creator {
			return a.ID
		}
	}
	return ""
}

// FindSession locates a thread's rollout file by id.
func FindSession(s core.Settings, id string) (Session, bool) {
	var hit Session
	for _, root := range sessionsDirs(s) {
		filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || hit.Path != "" {
				return nil
			}
			if strings.HasSuffix(d.Name(), id+".jsonl") {
				fi, _ := d.Info()
				m := readMeta(p)
				hit = Session{ID: id, Path: p, Cwd: m.Cwd, Creator: m.CreatorAccountID}
				if fi != nil {
					hit.Mod = fi.ModTime()
				}
				return fs.SkipAll
			}
			return nil
		})
	}
	return hit, hit.Path != ""
}

func idFromName(p string) string {
	name := strings.TrimSuffix(filepath.Base(p), ".jsonl")
	if len(name) >= 36 {
		return name[len(name)-36:]
	}
	return ""
}

// ScanSignals classifies the errors and limit hits a thread recorded after since.
func ScanSignals(path string, since time.Time) safety.Signal {
	var worst safety.Signal
	for _, line := range tailLines(path, 2<<20) {
		if !bytes.Contains(line, []byte(`"event_msg"`)) {
			continue
		}
		var ev eventLine
		if json.Unmarshal(line, &ev) != nil || ev.Timestamp.Before(since) {
			continue
		}
		var sig safety.Signal
		switch ev.Payload.Type {
		case "error", "stream_error":
			sig = safety.Classify("", ev.Payload.Message)
			if sig.Kind == safety.Limit {
				sig.Until = safety.ResetFrom(ev.Payload.Message, ev.Timestamp)
			}
		case "token_count":
			if rl := ev.Payload.RateLimits; rl != nil && rl.Reached != nil && rl.Reached != "" {
				sig = safety.Signal{Kind: safety.Limit, Reason: "Codex 报告额度已用完"}
			}
		}
		sig.At = ev.Timestamp
		if sig.Kind.Worse(worst.Kind) {
			worst = sig
		}
	}
	return worst
}

// EncryptedReplayFailed reports whether a resumed thread was rejected because
// the new account could not read the previous account's encrypted reasoning.
func EncryptedReplayFailed(path string, since time.Time) bool {
	for _, line := range tailLines(path, 2<<20) {
		if !bytes.Contains(line, []byte(`"event_msg"`)) {
			continue
		}
		var ev eventLine
		if json.Unmarshal(line, &ev) != nil || ev.Timestamp.Before(since) {
			continue
		}
		if ev.Payload.Type != "error" && ev.Payload.Type != "stream_error" {
			continue
		}
		m := strings.ToLower(ev.Payload.Message)
		if strings.Contains(m, "encrypted") || strings.Contains(m, "could not be verified") || strings.Contains(m, "decrypt") {
			return true
		}
	}
	return false
}
