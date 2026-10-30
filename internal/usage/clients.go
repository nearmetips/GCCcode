package usage

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

/* ---------- Claude Code ---------- */

// scanClaudeLine reads one transcript line. Claude writes one line per
// content block of an answer, each repeating the whole answer's usage under
// the same message id, so only the last line of a message counts.
func scanClaudeLine(f *file, line []byte) {
	if !bytes.Contains(line, []byte(`"usage"`)) {
		if f.Session == "" && bytes.Contains(line, []byte(`"sessionId"`)) {
			var head struct {
				SessionID string `json:"sessionId"`
				Cwd       string `json:"cwd"`
			}
			if json.Unmarshal(line, &head) == nil {
				f.Session, f.Cwd = head.SessionID, head.Cwd
			}
		}
		return
	}
	var rec struct {
		Type        string `json:"type"`
		Timestamp   string `json:"timestamp"`
		SessionID   string `json:"sessionId"`
		Cwd         string `json:"cwd"`
		IsSidechain bool   `json:"isSidechain"`
		APIError    bool   `json:"isApiErrorMessage"`
		Message     struct {
			ID    string `json:"id"`
			Model string `json:"model"`
			Usage *struct {
				Input      int64 `json:"input_tokens"`
				Output     int64 `json:"output_tokens"`
				CacheWrite int64 `json:"cache_creation_input_tokens"`
				CacheRead  int64 `json:"cache_read_input_tokens"`
			} `json:"usage"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &rec) != nil || rec.Type != "assistant" || rec.Message.Usage == nil {
		return
	}
	if f.Session == "" {
		f.Session, f.Cwd = rec.SessionID, rec.Cwd
	}
	if rec.IsSidechain {
		f.Sub = true
	}
	if rec.APIError || rec.Message.Model == "" || rec.Message.Model == "<synthetic>" {
		return
	}
	t, err := time.Parse(time.RFC3339Nano, rec.Timestamp)
	if err != nil {
		return
	}
	us := rec.Message.Usage
	u := Tokens{Input: us.Input, Output: us.Output, CacheRead: us.CacheRead, CacheWrite: us.CacheWrite, Calls: 1}
	if rec.Message.ID != "" && rec.Message.ID == f.LastID {
		cur := f.Use[f.LastKey]
		cur.Sub(f.LastUse)
		f.Use[f.LastKey] = cur
	}
	key := dayKey(t) + "|" + rec.Message.Model
	f.add(t, rec.Message.Model, u)
	f.LastID, f.LastKey, f.LastUse = rec.Message.ID, key, u
}

/* ---------- Codex ---------- */

// scanCodexLine reads one rollout line. token_count events carry the
// thread's running total; the difference from the previous event is what
// the call just made cost.
func scanCodexLine(f *file, line []byte) {
	switch {
	case f.Session == "" && bytes.Contains(line, []byte(`"session_meta"`)):
		var rec struct {
			Type    string `json:"type"`
			Payload struct {
				ID            string `json:"id"`
				Creator       string `json:"creator_account_id"`
				Cwd           string `json:"cwd"`
				Parent        string `json:"parent_thread_id"`
				ThreadSource  string `json:"thread_source"`
				ModelProvider string `json:"model_provider"`
			} `json:"payload"`
		}
		if json.Unmarshal(line, &rec) != nil || rec.Type != "session_meta" {
			return
		}
		p := rec.Payload
		f.Session, f.Creator, f.Cwd, f.Parent, f.Provider = p.ID, p.Creator, p.Cwd, p.Parent, p.ModelProvider
		if p.Parent != "" || p.ThreadSource == "subagent" || strings.Contains(p.ThreadSource, "guardian") {
			f.Sub = true
		}
	case bytes.Contains(line, []byte(`"turn_context"`)):
		var rec struct {
			Payload struct {
				Model string `json:"model"`
			} `json:"payload"`
		}
		if json.Unmarshal(line, &rec) == nil && rec.Payload.Model != "" {
			f.Model = rec.Payload.Model
		}
	case bytes.Contains(line, []byte(`"token_count"`)):
		var rec struct {
			Timestamp time.Time `json:"timestamp"`
			Payload   struct {
				Type string `json:"type"`
				Info *struct {
					Total *struct {
						Input      int64 `json:"input_tokens"`
						Cached     int64 `json:"cached_input_tokens"`
						CacheWrite int64 `json:"cache_write_input_tokens"`
						Output     int64 `json:"output_tokens"`
					} `json:"total_token_usage"`
				} `json:"info"`
			} `json:"payload"`
		}
		if json.Unmarshal(line, &rec) != nil || rec.Payload.Type != "token_count" || rec.Payload.Info == nil || rec.Payload.Info.Total == nil {
			return
		}
		tt := rec.Payload.Info.Total
		cur := Tokens{Input: tt.Input - tt.Cached, CacheRead: tt.Cached, CacheWrite: tt.CacheWrite, Output: tt.Output}
		delta := cur
		delta.Sub(f.Prev)
		if delta.Input < 0 || delta.Output < 0 || delta.CacheRead < 0 || delta.CacheWrite < 0 {
			delta = cur // the counter started over
		}
		f.Prev = cur
		if delta.Zero() {
			return
		}
		delta.Calls = 1
		model := f.Model
		if model == "" {
			model = "codex"
		}
		f.add(rec.Timestamp, model, delta)
	}
}

/* ---------- Grok CLI ---------- */

// readGrokSummary reads the session's summary.json next to updates.jsonl.
func readGrokSummary(f *file) {
	b, err := os.ReadFile(filepath.Join(filepath.Dir(f.Path), "summary.json"))
	if err != nil {
		return
	}
	var sum struct {
		Info struct {
			ID  string `json:"id"`
			Cwd string `json:"cwd"`
		} `json:"info"`
		AgentID string `json:"agent_id"`
		Model   string `json:"current_model_id"`
	}
	if json.Unmarshal(b, &sum) != nil {
		return
	}
	f.Session, f.Cwd, f.Model = sum.Info.ID, sum.Info.Cwd, sum.Model
	if f.Session == "" {
		f.Session = filepath.Base(filepath.Dir(f.Path))
	}
	if sum.AgentID != "" {
		f.Sub = true
	}
}

// scanGrokLine reads one session update. turn_completed carries that
// turn's usage per model.
func scanGrokLine(f *file, line []byte) {
	if !bytes.Contains(line, []byte(`"turn_completed"`)) {
		return
	}
	var rec struct {
		Timestamp int64 `json:"timestamp"`
		Params    struct {
			Update struct {
				Kind  string `json:"sessionUpdate"`
				Usage *struct {
					Models map[string]struct {
						Input      int64 `json:"inputTokens"`
						Output     int64 `json:"outputTokens"`
						CacheRead  int64 `json:"cachedReadTokens"`
						CacheWrite int64 `json:"cacheCreationTokens"`
						Calls      int64 `json:"modelCalls"`
					} `json:"modelUsage"`
					Input      int64 `json:"inputTokens"`
					Output     int64 `json:"outputTokens"`
					CacheRead  int64 `json:"cachedReadTokens"`
					CacheWrite int64 `json:"cacheCreationTokens"`
					Calls      int64 `json:"modelCalls"`
				} `json:"usage"`
			} `json:"update"`
		} `json:"params"`
	}
	if json.Unmarshal(line, &rec) != nil || rec.Params.Update.Kind != "turn_completed" || rec.Params.Update.Usage == nil {
		return
	}
	t := time.Unix(rec.Timestamp, 0)
	if rec.Timestamp > 1e12 {
		t = time.UnixMilli(rec.Timestamp)
	}
	us := rec.Params.Update.Usage
	if len(us.Models) == 0 {
		model := f.Model
		if model == "" {
			model = "grok"
		}
		f.add(t, model, Tokens{Input: us.Input - us.CacheRead, Output: us.Output, CacheRead: us.CacheRead, CacheWrite: us.CacheWrite, Calls: us.Calls})
		return
	}
	for model, m := range us.Models {
		f.add(t, model, Tokens{Input: m.Input - m.CacheRead, Output: m.Output, CacheRead: m.CacheRead, CacheWrite: m.CacheWrite, Calls: m.Calls})
	}
}

/* ---------- Gemini CLI ---------- */

// readGemini reads a whole chat file; Gemini rewrites it on every turn.
func readGemini(f *file) error {
	b, err := os.ReadFile(f.Path)
	if err != nil {
		return err
	}
	var chat struct {
		SessionID string `json:"sessionId"`
		Messages  []struct {
			Type      string `json:"type"`
			Role      string `json:"role"`
			Timestamp string `json:"timestamp"`
			Model     string `json:"model"`
			Content   any    `json:"content"`
			Tokens    *struct {
				Input    int64 `json:"input"`
				Output   int64 `json:"output"`
				Cached   int64 `json:"cached"`
				Thoughts int64 `json:"thoughts"`
				Tool     int64 `json:"tool"`
			} `json:"tokens"`
		} `json:"messages"`
	}
	if json.Unmarshal(b, &chat) != nil {
		return nil
	}
	f.Session = chat.SessionID
	if f.Session == "" {
		f.Session = strings.TrimSuffix(filepath.Base(f.Path), ".json")
	}
	for _, m := range chat.Messages {
		if f.Title == "" && (m.Type == "user" || m.Role == "user") {
			if s, ok := m.Content.(string); ok {
				f.Title = oneLine(s, 60)
			}
		}
		if m.Tokens == nil {
			continue
		}
		t, err := time.Parse(time.RFC3339Nano, m.Timestamp)
		if err != nil {
			continue
		}
		model := m.Model
		if model == "" {
			model = "gemini"
		}
		in := m.Tokens.Input - m.Tokens.Cached
		if in < 0 {
			in = m.Tokens.Input
		}
		f.add(t, model, Tokens{Input: in, Output: m.Tokens.Output + m.Tokens.Thoughts + m.Tokens.Tool, CacheRead: m.Tokens.Cached, Calls: 1})
	}
	return nil
}

func oneLine(s string, limit int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit]) + "…"
}
