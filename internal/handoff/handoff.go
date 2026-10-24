// Package handoff builds a plain-text continuation note from a local
// transcript, used when the next account cannot resume the original session.
package handoff

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nearmetips/DailiCode/internal/core"
)

// Prompt hands a note to the next session. A session started this way takes
// its goal from that note, so a chain of handoffs keeps the original task.
const Prompt = "请阅读这个接力文件并接着做："

const (
	maxRunes        = 12000
	maxTurnRunes    = 3000
	maxGoalRunes    = 3000
	maxSummaryRunes = 5000
	maxFiles        = 50
	maxGitLines     = 40
)

const (
	goalHeading    = "任务目标"
	summaryHeading = "更早的对话摘要"
	todoHeading    = "待办（最后一次更新）"
	filesHeading   = "这个会话改过的文件"
	gitHeading     = "工作区未提交的改动"
	turnsHeading   = "最近的对话"
)

type turn struct {
	role string
	text string
}

type todo struct {
	text   string
	status string
}

// record is what a transcript can tell the next session.
type record struct {
	cwd     string
	summary string
	todos   []todo
	files   []string
	turns   []turn
}

func (r *record) addFile(path string) {
	path = strings.TrimSpace(path)
	if path == "" {
		return
	}
	for _, f := range r.files {
		if f == path {
			return
		}
	}
	r.files = append(r.files, path)
}

// addTurn drops a repeat of the previous turn: Codex logs each user message
// and each final answer twice, once per event stream.
func (r *record) addTurn(role, text string) {
	if text == "" {
		return
	}
	if n := len(r.turns); n > 0 && r.turns[n-1].role == role && r.turns[n-1].text == text {
		return
	}
	r.turns = append(r.turns, turn{role: role, text: text})
}

// WriteClaude reads a Claude Code transcript and writes a handoff note.
func WriteClaude(transcript, sessionID string) (string, error) {
	r, err := claudeRecord(transcript)
	if err != nil {
		return "", err
	}
	return write("claude", sessionID, r)
}

// WriteCodex reads a Codex rollout and writes a handoff note.
func WriteCodex(rollout, sessionID string) (string, error) {
	r, err := codexRecord(rollout)
	if err != nil {
		return "", err
	}
	return write("codex", sessionID, r)
}

// WriteGrok reads a Grok session directory (or its chat_history.jsonl) and
// writes a handoff note.
func WriteGrok(sessionDirOrChat, sessionID string) (string, error) {
	r, err := grokRecord(sessionDirOrChat)
	if err != nil {
		return "", err
	}
	return write("grok", sessionID, r)
}

// WriteGemini reads a Gemini chat file and writes a handoff note.
func WriteGemini(chatPath, sessionID string) (string, error) {
	r, err := geminiRecord(chatPath)
	if err != nil {
		return "", err
	}
	return write("gemini", sessionID, r)
}

func write(kind, sessionID string, r record) (string, error) {
	if err := os.MkdirAll(core.HandoffDir(), 0o700); err != nil {
		return "", err
	}
	name := fmt.Sprintf("%s-%s-%s.md", kind, safe(sessionID), time.Now().Format("20060102-150405"))
	path := filepath.Join(core.HandoffDir(), name)
	if err := os.WriteFile(path, []byte(render(sessionID, r)), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func render(sessionID string, r record) string {
	var b strings.Builder
	b.WriteString("# GCC Code 会话接力\n\n")
	b.WriteString("上一个账号的会话无法直接续上（常见原因是服务端加密的推理内容不能跨账号读取）。")
	b.WriteString("下面是从本地记录整理的交接信息。先核对工作区的实际状态，再接着做，不要重复已经完成的部分。\n\n")
	if sessionID != "" {
		b.WriteString("- 原会话：`" + sessionID + "`\n")
	}
	if r.cwd != "" {
		b.WriteString("- 工作目录：`" + r.cwd + "`\n")
	}
	if sessionID != "" || r.cwd != "" {
		b.WriteString("\n")
	}
	goal, rest := goalOf(r.turns)
	if goal != "" {
		section(&b, goalHeading, clip(goal, maxGoalRunes))
	}
	if r.summary != "" {
		section(&b, summaryHeading, clip(r.summary, maxSummaryRunes))
	}
	if len(r.todos) > 0 {
		section(&b, todoHeading, todoList(r.todos))
	}
	if len(r.files) > 0 {
		section(&b, filesHeading, fileList(r.cwd, r.files))
	}
	if st := gitStatus(r.cwd); st != "" {
		section(&b, gitHeading, "```text\n"+st+"\n```")
	}
	body := tail(rest)
	if body == "" && goal == "" {
		body = "（没有从本地记录里提取到对话正文）"
	}
	if body != "" {
		section(&b, turnsHeading, body)
	}
	return b.String()
}

func section(b *strings.Builder, heading, body string) {
	b.WriteString("## " + heading + "\n\n")
	b.WriteString(strings.TrimRight(body, "\n"))
	b.WriteString("\n\n")
}

// goalOf is the first user message and the turns after it.
func goalOf(turns []turn) (string, []turn) {
	for i, t := range turns {
		if t.role == "user" {
			return resolveGoal(t.text), turns[i+1:]
		}
	}
	return "", turns
}

// resolveGoal is the task behind a first message. When that message only
// hands over an earlier note, the earlier note's goal is the real one.
func resolveGoal(text string) string {
	j := strings.Index(text, Prompt)
	if j < 0 {
		return text
	}
	path := strings.TrimSpace(text[j+len(Prompt):])
	if k := strings.IndexByte(path, '\n'); k >= 0 {
		path = strings.TrimSpace(path[:k])
	}
	if prev := goalFromNote(path); prev != "" {
		return prev
	}
	return text
}

// Title is a session's first request on one short line, for listings; ""
// when the user never said anything in it. path may be a transcript file or
// a Grok session directory that has summary.json.
func Title(path string) string {
	if fi, err := os.Stat(path); err == nil && fi.IsDir() {
		if b, err := os.ReadFile(filepath.Join(path, "summary.json")); err == nil {
			var sum struct {
				SessionSummary string `json:"session_summary"`
			}
			if json.Unmarshal(b, &sum) == nil && strings.TrimSpace(sum.SessionSummary) != "" {
				return oneLine(sum.SessionSummary, 60)
			}
		}
		path = filepath.Join(path, "chat_history.jsonl")
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 16<<20)
	for n := 0; n < 500 && sc.Scan(); n++ {
		line := sc.Bytes()
		if !bytes.Contains(line, []byte(`"user"`)) && !bytes.Contains(line, []byte(`"UserMessage"`)) {
			continue
		}
		if text := userText(line); text != "" {
			return oneLine(resolveGoal(text), 60)
		}
	}
	return ""
}

// userText is what the user typed in one transcript line of either client,
// or "" when the line is not a user message.
func userText(line []byte) string {
	var rec struct {
		Type        string          `json:"type"`
		Role        string          `json:"role"`
		IsMeta      bool            `json:"isMeta"`
		IsSidechain bool            `json:"isSidechain"`
		Content     json.RawMessage `json:"content"`
		Message     struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
		Payload struct {
			Type    string          `json:"type"`
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
			Item    struct {
				Type    string          `json:"type"`
				Content json.RawMessage `json:"content"`
			} `json:"item"`
		} `json:"payload"`
	}
	if json.Unmarshal(line, &rec) != nil {
		return ""
	}
	p := rec.Payload
	switch {
	case rec.Type == "user" && !rec.IsMeta && !rec.IsSidechain:
		if t := cleanUser(contentText(rec.Message.Content)); t != "" {
			return t
		}
		return cleanGrokUser(contentText(rec.Content))
	case rec.Role == "user" && rec.Type == "":
		return cleanGrokUser(contentText(rec.Content))
	case rec.Type == "response_item" && p.Type == "message" && p.Role == "user":
		return cleanUser(contentText(p.Content))
	case rec.Type == "event_msg" && p.Type == "item_completed" && p.Item.Type == "UserMessage":
		return cleanUser(contentText(p.Item.Content))
	}
	return ""
}

// cleanGrokUser strips the XML context blocks Grok injects around a request.
func cleanGrokUser(text string) string {
	s := strings.TrimSpace(text)
	for {
		if !strings.HasPrefix(s, "<") {
			break
		}
		end := strings.IndexAny(s, " >")
		if end < 2 {
			break
		}
		tag := s[1:end]
		closing := "</" + tag + ">"
		i := strings.Index(s, closing)
		if i < 0 {
			break
		}
		s = strings.TrimSpace(s[i+len(closing):])
	}
	return cleanUser(s)
}

func oneLine(s string, limit int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit]) + "…"
}

func goalFromNote(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	s := string(b)
	head := "## " + goalHeading + "\n\n"
	i := strings.Index(s, head)
	if i < 0 {
		return ""
	}
	s = s[i+len(head):]
	end := len(s)
	for _, h := range []string{summaryHeading, todoHeading, filesHeading, gitHeading, turnsHeading} {
		if j := strings.Index(s, "\n## "+h+"\n"); j >= 0 && j < end {
			end = j
		}
	}
	return strings.TrimSpace(s[:end])
}

func tail(turns []turn) string {
	var parts []string
	total := 0
	for i := len(turns) - 1; i >= 0; i-- {
		t := turns[i]
		label := "助手"
		if t.role == "user" {
			label = "用户"
			if i == len(turns)-1 {
				label = "用户（上一个号还没回应这条）"
			}
		}
		chunk := "### " + label + "\n\n" + clip(t.text, maxTurnRunes) + "\n\n"
		n := utf8.RuneCountInString(chunk)
		if total+n > maxRunes && len(parts) > 0 {
			break
		}
		parts = append(parts, chunk)
		total += n
	}
	for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
		parts[i], parts[j] = parts[j], parts[i]
	}
	return strings.Join(parts, "")
}

// clip keeps the start and the end of a long text, where the request and
// the conclusion usually are.
func clip(s string, limit int) string {
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	half := limit / 2
	return string(r[:half]) + fmt.Sprintf("\n\n…（中间省略 %d 字）…\n\n", len(r)-2*half) + string(r[len(r)-half:])
}

func todoList(todos []todo) string {
	var b strings.Builder
	for _, t := range todos {
		mark, note := "[ ]", ""
		switch t.status {
		case "completed":
			mark = "[x]"
		case "in_progress":
			note = "（进行中）"
		}
		b.WriteString("- " + mark + " " + t.text + note + "\n")
	}
	return b.String()
}

func fileList(cwd string, files []string) string {
	var b strings.Builder
	for i, f := range files {
		if i == maxFiles {
			fmt.Fprintf(&b, "- …另有 %d 个\n", len(files)-maxFiles)
			break
		}
		if cwd != "" && filepath.IsAbs(f) {
			if rel, err := filepath.Rel(cwd, f); err == nil && !strings.HasPrefix(rel, "..") {
				f = rel
			}
		}
		b.WriteString("- `" + f + "`\n")
	}
	return b.String()
}

// gitStatus lists uncommitted changes in dir. It covers edits a transcript
// does not show, such as those made by subagents or shell commands.
func gitStatus(dir string) string {
	if dir == "" {
		return ""
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "status", "--porcelain=v1", "--untracked-files=normal")
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	text := strings.TrimRight(string(out), "\n")
	if text == "" {
		return ""
	}
	lines := strings.Split(text, "\n")
	if len(lines) > maxGitLines {
		more := len(lines) - maxGitLines
		lines = append(lines[:maxGitLines], fmt.Sprintf("…另有 %d 项", more))
	}
	return strings.Join(lines, "\n")
}

func safe(id string) string {
	if id == "" {
		return "session"
	}
	var b strings.Builder
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		}
	}
	s := b.String()
	if len(s) > 40 {
		s = s[:40]
	}
	if s == "" {
		return "session"
	}
	return s
}

// cleanUser strips what the client injects around a user's own words:
// context blocks such as <system-reminder> or <in-app-browser-context>,
// slash-command echoes, and AGENTS.md instructions.
func cleanUser(text string) string {
	s := strings.TrimSpace(text)
	for strings.HasPrefix(s, "<") {
		end := strings.IndexAny(s, " >")
		if end < 2 {
			break
		}
		tag := s[1:end]
		if !strings.ContainsAny(tag, "-_") && tag != "INSTRUCTIONS" {
			break
		}
		closing := "</" + tag + ">"
		i := strings.Index(s, closing)
		if i < 0 {
			break
		}
		s = strings.TrimSpace(s[i+len(closing):])
	}
	s = strings.TrimSpace(strings.TrimPrefix(s, "## My request:"))
	for _, p := range []string{"# AGENTS.md instructions", "Caveat: The messages below", "[Request interrupted"} {
		if strings.HasPrefix(s, p) {
			return ""
		}
	}
	return s
}

const answeredPrefix = "User has answered your questions:"

func claudeRecord(path string) (record, error) {
	f, err := os.Open(path)
	if err != nil {
		return record{}, err
	}
	defer f.Close()
	var r record
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 16<<20)
	for sc.Scan() {
		var rec struct {
			Type             string `json:"type"`
			Cwd              string `json:"cwd"`
			IsMeta           bool   `json:"isMeta"`
			IsSidechain      bool   `json:"isSidechain"`
			IsAPIError       bool   `json:"isApiErrorMessage"`
			IsCompactSummary bool   `json:"isCompactSummary"`
			Message          struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(sc.Bytes(), &rec) != nil {
			continue
		}
		if r.cwd == "" && rec.Cwd != "" {
			r.cwd = rec.Cwd
		}
		if rec.Type != "user" && rec.Type != "assistant" {
			continue
		}
		if rec.IsSidechain || rec.IsAPIError {
			continue
		}
		if rec.IsCompactSummary {
			r.summary = contentText(rec.Message.Content)
			continue
		}
		if rec.IsMeta {
			continue
		}
		role := rec.Message.Role
		if role == "" {
			role = rec.Type
		}
		claudeTools(&r, rec.Message.Content)
		text := contentText(rec.Message.Content)
		if role == "user" {
			text = cleanUser(text)
		}
		r.addTurn(role, text)
	}
	return r, sc.Err()
}

// claudeTools picks the todo list, edited files and the user's answers to
// questions out of tool calls and results.
func claudeTools(r *record, raw json.RawMessage) {
	var parts []struct {
		Type    string          `json:"type"`
		Name    string          `json:"name"`
		Input   json.RawMessage `json:"input"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return
	}
	for _, p := range parts {
		switch p.Type {
		case "tool_use":
			switch p.Name {
			case "TodoWrite":
				var in struct {
					Todos []struct {
						Content string `json:"content"`
						Status  string `json:"status"`
					} `json:"todos"`
				}
				if json.Unmarshal(p.Input, &in) == nil {
					r.todos = r.todos[:0]
					for _, t := range in.Todos {
						r.todos = append(r.todos, todo{text: t.Content, status: t.Status})
					}
				}
			case "Write", "Edit", "MultiEdit", "NotebookEdit":
				var in struct {
					FilePath     string `json:"file_path"`
					NotebookPath string `json:"notebook_path"`
				}
				if json.Unmarshal(p.Input, &in) == nil {
					r.addFile(in.FilePath)
					r.addFile(in.NotebookPath)
				}
			}
		case "tool_result":
			if text := contentText(p.Content); strings.HasPrefix(text, answeredPrefix) {
				r.addTurn("user", text)
			}
		}
	}
}

// patchFile finds the files an apply_patch touches, whether it was called
// directly or from inside an exec script.
var patchFile = regexp.MustCompile("\\*\\*\\* (?:Add|Update|Delete) File: ([^\\n\\\\\"'`]+)|\\*\\*\\* Move to: ([^\\n\\\\\"'`]+)")

func codexRecord(path string) (record, error) {
	f, err := os.Open(path)
	if err != nil {
		return record{}, err
	}
	defer f.Close()
	var r record
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 16<<20)
	for sc.Scan() {
		var rec struct {
			Type    string `json:"type"`
			Payload struct {
				Type             string          `json:"type"`
				Role             string          `json:"role"`
				Name             string          `json:"name"`
				Cwd              string          `json:"cwd"`
				Message          string          `json:"message"`
				Arguments        string          `json:"arguments"`
				Input            string          `json:"input"`
				Content          json.RawMessage `json:"content"`
				LastAgentMessage string          `json:"last_agent_message"`
				Item             struct {
					Type    string                     `json:"type"`
					Content json.RawMessage            `json:"content"`
					Changes map[string]json.RawMessage `json:"changes"`
				} `json:"item"`
			} `json:"payload"`
		}
		if json.Unmarshal(sc.Bytes(), &rec) != nil {
			continue
		}
		p := rec.Payload
		switch {
		case rec.Type == "session_meta":
			if r.cwd == "" {
				r.cwd = p.Cwd
			}
		case rec.Type == "compacted":
			if s := strings.TrimSpace(p.Message); s != "" {
				r.summary = s
			}
		case rec.Type == "response_item" && p.Type == "message":
			if p.Role == "developer" || p.Role == "system" {
				continue
			}
			role := p.Role
			if role == "" {
				role = "assistant"
			}
			text := contentText(p.Content)
			if role == "user" {
				text = cleanUser(text)
			}
			r.addTurn(role, text)
		case rec.Type == "response_item" && p.Type == "agent_message":
			// Messages between subagents start with a routing header and
			// are mostly encrypted; only the main agent's own count.
			if text := contentText(p.Content); !strings.HasPrefix(text, "Message Type:") {
				r.addTurn("assistant", text)
			}
		case rec.Type == "response_item" && (p.Type == "function_call" || p.Type == "custom_tool_call"):
			if p.Name == "update_plan" {
				var in struct {
					Plan []struct {
						Step   string `json:"step"`
						Status string `json:"status"`
					} `json:"plan"`
				}
				if json.Unmarshal([]byte(p.Arguments), &in) == nil {
					r.todos = r.todos[:0]
					for _, t := range in.Plan {
						r.todos = append(r.todos, todo{text: t.Step, status: t.Status})
					}
				}
			}
			for _, m := range patchFile.FindAllStringSubmatch(p.Input+"\n"+p.Arguments, -1) {
				r.addFile(m[1] + m[2])
			}
		case rec.Type == "event_msg" && p.Type == "item_completed":
			switch p.Item.Type {
			case "UserMessage":
				r.addTurn("user", cleanUser(contentText(p.Item.Content)))
			case "FileChange":
				names := make([]string, 0, len(p.Item.Changes))
				for name := range p.Item.Changes {
					names = append(names, name)
				}
				sort.Strings(names)
				for _, name := range names {
					r.addFile(name)
				}
			}
		case rec.Type == "event_msg" && p.Type == "task_complete":
			r.addTurn("assistant", strings.TrimSpace(p.LastAgentMessage))
		}
	}
	return r, sc.Err()
}

func contentText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s)
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var b strings.Builder
	for _, p := range parts {
		if p.Text == "" || p.Type == "encrypted_content" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(strings.TrimSpace(p.Text))
	}
	return strings.TrimSpace(b.String())
}

func grokRecord(path string) (record, error) {
	chat := path
	var r record
	if fi, err := os.Stat(path); err == nil && fi.IsDir() {
		if b, err := os.ReadFile(filepath.Join(path, "summary.json")); err == nil {
			var sum struct {
				Info struct {
					Cwd string `json:"cwd"`
				} `json:"info"`
				SessionSummary string `json:"session_summary"`
			}
			if json.Unmarshal(b, &sum) == nil {
				r.cwd = sum.Info.Cwd
				r.summary = strings.TrimSpace(sum.SessionSummary)
			}
		}
		if b, err := os.ReadFile(filepath.Join(path, "resources_state.json")); err == nil {
			var rs struct {
				State map[string]json.RawMessage `json:"state"`
			}
			if json.Unmarshal(b, &rs) == nil {
				if raw, ok := rs.State["grok_build.Todo"]; ok {
					var box struct {
						Todos map[string]struct {
							Content string `json:"content"`
							Status  string `json:"status"`
						} `json:"todos"`
					}
					if json.Unmarshal(raw, &box) == nil {
						for _, t := range box.Todos {
							r.todos = append(r.todos, todo{text: t.Content, status: t.Status})
						}
					}
				}
			}
		}
		chat = filepath.Join(path, "chat_history.jsonl")
	}
	f, err := os.Open(chat)
	if err != nil {
		if r.summary != "" {
			return r, nil
		}
		return record{}, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 16<<20)
	for sc.Scan() {
		var rec struct {
			Type    string          `json:"type"`
			Content json.RawMessage `json:"content"`
		}
		if json.Unmarshal(sc.Bytes(), &rec) != nil {
			continue
		}
		switch rec.Type {
		case "user":
			r.addTurn("user", cleanGrokUser(contentText(rec.Content)))
		case "assistant":
			r.addTurn("assistant", contentText(rec.Content))
		}
	}
	if len(r.turns) == 0 && r.summary != "" {
		r.addTurn("user", r.summary)
	}
	return r, sc.Err()
}

func geminiRecord(path string) (record, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return record{}, err
	}
	var r record
	var meta struct {
		Cwd        string `json:"cwd"`
		WorkingDir string `json:"working_directory"`
		Title      string `json:"title"`
		Messages   []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal(b, &meta) == nil && len(meta.Messages) > 0 {
		r.cwd = meta.Cwd
		if r.cwd == "" {
			r.cwd = meta.WorkingDir
		}
		if meta.Title != "" {
			r.summary = meta.Title
		}
		for _, m := range meta.Messages {
			role := m.Role
			if role == "model" {
				role = "assistant"
			}
			text := contentText(m.Content)
			if role == "user" {
				text = cleanUser(text)
			}
			r.addTurn(role, text)
		}
		return r, nil
	}
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, 1<<20), 16<<20)
	for sc.Scan() {
		var rec struct {
			Type    string          `json:"type"`
			Role    string          `json:"role"`
			Cwd     string          `json:"cwd"`
			Content json.RawMessage `json:"content"`
		}
		if json.Unmarshal(sc.Bytes(), &rec) != nil {
			continue
		}
		if r.cwd == "" && rec.Cwd != "" {
			r.cwd = rec.Cwd
		}
		role := rec.Role
		if role == "" {
			role = rec.Type
		}
		if role == "model" {
			role = "assistant"
		}
		if role != "user" && role != "assistant" {
			continue
		}
		text := contentText(rec.Content)
		if role == "user" {
			text = cleanUser(text)
		}
		r.addTurn(role, text)
	}
	return r, sc.Err()
}
