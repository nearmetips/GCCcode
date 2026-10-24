package gateway

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Anthropic Messages request, as much of it as maps onto Chat Completions.
type anthReq struct {
	Model         string          `json:"model"`
	System        json.RawMessage `json:"system"`
	Messages      []anthMsg       `json:"messages"`
	MaxTokens     int             `json:"max_tokens"`
	Temperature   *float64        `json:"temperature"`
	TopP          *float64        `json:"top_p"`
	StopSequences []string        `json:"stop_sequences"`
	Stream        bool            `json:"stream"`
	Tools         []anthTool      `json:"tools"`
	ToolChoice    *anthToolChoice `json:"tool_choice"`
}

type anthMsg struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type anthBlock struct {
	Type   string `json:"type"`
	Text   string `json:"text"`
	Source *struct {
		Type      string `json:"type"`
		MediaType string `json:"media_type"`
		Data      string `json:"data"`
		URL       string `json:"url"`
	} `json:"source"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

type anthTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type anthToolChoice struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

// Chat Completions request.
type chatReq struct {
	Model         string         `json:"model"`
	Messages      []chatMsg      `json:"messages"`
	MaxTokens     int            `json:"max_tokens,omitempty"`
	Temperature   *float64       `json:"temperature,omitempty"`
	TopP          *float64       `json:"top_p,omitempty"`
	Stop          []string       `json:"stop,omitempty"`
	Stream        bool           `json:"stream,omitempty"`
	StreamOptions *streamOptions `json:"stream_options,omitempty"`
	Tools         []chatTool     `json:"tools,omitempty"`
	ToolChoice    any            `json:"tool_choice,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type chatMsg struct {
	Role       string         `json:"role"`
	Content    any            `json:"content"`
	ToolCalls  []chatToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type chatPart struct {
	Type     string        `json:"type"`
	Text     string        `json:"text,omitempty"`
	ImageURL *chatImageURL `json:"image_url,omitempty"`
}

type chatImageURL struct {
	URL string `json:"url"`
}

type chatToolCall struct {
	Index    *int   `json:"index,omitempty"`
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	} `json:"function"`
}

type chatTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

func anthBlocks(raw json.RawMessage) ([]anthBlock, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return []anthBlock{{Type: "text", Text: s}}, nil
	}
	var bs []anthBlock
	if err := json.Unmarshal(raw, &bs); err != nil {
		return nil, errors.New("content 既不是字符串也不是内容块数组")
	}
	return bs, nil
}

func joinText(bs []anthBlock) string {
	var parts []string
	for _, b := range bs {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n\n")
}

// anthropicToChat translates a Messages request. model, when set, replaces
// the requested one: Claude model names mean nothing to other vendors. It
// returns the agent's own model name so answers can echo it back.
func anthropicToChat(body []byte, model string) (out []byte, stream bool, agentModel string, err error) {
	var in anthReq
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, false, "", errors.New("不是合法的 Anthropic Messages 请求")
	}
	cr := chatReq{
		Model: in.Model, MaxTokens: in.MaxTokens, Temperature: in.Temperature, TopP: in.TopP,
		Stop: in.StopSequences, Stream: in.Stream,
	}
	if model != "" {
		cr.Model = model
	}
	if in.Stream {
		cr.StreamOptions = &streamOptions{IncludeUsage: true}
	}
	sys, err := anthBlocks(in.System)
	if err != nil {
		return nil, false, "", fmt.Errorf("system：%w", err)
	}
	if text := joinText(sys); text != "" {
		cr.Messages = append(cr.Messages, chatMsg{Role: "system", Content: text})
	}
	for _, m := range in.Messages {
		bs, err := anthBlocks(m.Content)
		if err != nil {
			return nil, false, "", err
		}
		switch m.Role {
		case "assistant":
			cr.Messages = append(cr.Messages, assistantMsg(bs))
		default:
			msgs, err := userMsgs(bs)
			if err != nil {
				return nil, false, "", err
			}
			cr.Messages = append(cr.Messages, msgs...)
		}
	}
	for _, t := range in.Tools {
		if t.Type != "" && t.Type != "custom" {
			continue // server-side tools (web search...) have no Chat equivalent
		}
		var ct chatTool
		ct.Type = "function"
		ct.Function.Name, ct.Function.Description = t.Name, t.Description
		ct.Function.Parameters = t.InputSchema
		if len(ct.Function.Parameters) == 0 {
			ct.Function.Parameters = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		cr.Tools = append(cr.Tools, ct)
	}
	if tc := in.ToolChoice; tc != nil && len(cr.Tools) > 0 {
		switch tc.Type {
		case "auto":
			cr.ToolChoice = "auto"
		case "any":
			cr.ToolChoice = "required"
		case "none":
			cr.ToolChoice = "none"
		case "tool":
			cr.ToolChoice = map[string]any{"type": "function", "function": map[string]string{"name": tc.Name}}
		}
	}
	out, err = json.Marshal(cr)
	return out, in.Stream, in.Model, err
}

// assistantMsg folds text and tool_use blocks into one assistant message.
// Thinking blocks are dropped: other vendors cannot verify their signatures.
func assistantMsg(bs []anthBlock) chatMsg {
	msg := chatMsg{Role: "assistant"}
	var text []string
	for _, b := range bs {
		switch b.Type {
		case "text":
			if b.Text != "" {
				text = append(text, b.Text)
			}
		case "tool_use":
			args := string(b.Input)
			if args == "" || args == "null" {
				args = "{}"
			}
			var tc chatToolCall
			tc.ID, tc.Type = b.ID, "function"
			tc.Function.Name, tc.Function.Arguments = b.Name, args
			msg.ToolCalls = append(msg.ToolCalls, tc)
		}
	}
	if len(text) > 0 || len(msg.ToolCalls) == 0 {
		msg.Content = strings.Join(text, "\n\n")
	}
	return msg
}

// userMsgs turns one user turn into Chat messages: each tool_result becomes
// its own "tool" message, which Chat requires to follow the assistant's tool
// calls directly; the rest becomes one user message.
func userMsgs(bs []anthBlock) ([]chatMsg, error) {
	var out []chatMsg
	var parts []chatPart
	hasImage := false
	for _, b := range bs {
		switch b.Type {
		case "tool_result":
			inner, err := anthBlocks(b.Content)
			if err != nil {
				return nil, fmt.Errorf("tool_result：%w", err)
			}
			text := joinText(inner)
			for _, ib := range inner {
				if ib.Type == "image" {
					text += "\n[图片结果，当前上游不支持在工具结果里传图]"
				}
			}
			if b.IsError {
				text = "Error: " + text
			}
			out = append(out, chatMsg{Role: "tool", ToolCallID: b.ToolUseID, Content: text})
		case "text":
			if b.Text != "" {
				parts = append(parts, chatPart{Type: "text", Text: b.Text})
			}
		case "image":
			if u := imageURL(b); u != "" {
				parts = append(parts, chatPart{Type: "image_url", ImageURL: &chatImageURL{URL: u}})
				hasImage = true
			}
		}
	}
	switch {
	case len(parts) == 0:
	case hasImage:
		out = append(out, chatMsg{Role: "user", Content: parts})
	default:
		var text []string
		for _, p := range parts {
			text = append(text, p.Text)
		}
		out = append(out, chatMsg{Role: "user", Content: strings.Join(text, "\n\n")})
	}
	return out, nil
}

func imageURL(b anthBlock) string {
	if b.Source == nil {
		return ""
	}
	switch b.Source.Type {
	case "base64":
		return "data:" + b.Source.MediaType + ";base64," + b.Source.Data
	case "url":
		return b.Source.URL
	}
	return ""
}

func stopReason(finish string) string {
	switch finish {
	case "length":
		return "max_tokens"
	case "tool_calls", "function_call":
		return "tool_use"
	default:
		return "end_turn"
	}
}

func newID(prefix string) string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

// Chat Completions response, non-streaming.
type chatResp struct {
	Choices []chatChoice `json:"choices"`
	Usage   *chatUsage   `json:"usage"`
}

type chatChoice struct {
	Message struct {
		Content   *string        `json:"content"`
		ToolCalls []chatToolCall `json:"tool_calls"`
	} `json:"message"`
	FinishReason string `json:"finish_reason"`
}

type chatUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

// anthMessage is an Anthropic Messages response.
type anthMessage struct {
	ID           string           `json:"id"`
	Type         string           `json:"type"`
	Role         string           `json:"role"`
	Model        string           `json:"model"`
	Content      []map[string]any `json:"content"`
	StopReason   string           `json:"stop_reason"`
	StopSequence *string          `json:"stop_sequence"`
	Usage        map[string]int   `json:"usage"`
}

func chatJSONToMessage(b []byte, model string) (anthMessage, error) {
	var cr chatResp
	if err := json.Unmarshal(b, &cr); err != nil {
		return anthMessage{}, errors.New("上游返回的不是合法的 Chat Completions JSON")
	}
	return chatRespToMessage(cr, model), nil
}

// collectChatStream gathers a streamed Chat answer into one response, for
// upstreams that stream even when the agent asked for a single JSON answer.
func collectChatStream(body io.Reader) (chatResp, error) {
	var (
		cr     chatResp
		choice chatChoice
		text   strings.Builder
		calls  []chatToolCall
		at     = map[int]int{}
		failed error
	)
	err := chatChunks(body, func(ch chatChunk) bool {
		if ch.Error != nil {
			b, _ := json.Marshal(map[string]any{"error": ch.Error})
			failed = errors.New(upstreamMessage(b, 0))
			return false
		}
		if ch.Usage != nil {
			cr.Usage = ch.Usage
		}
		for _, c := range ch.Choices {
			text.WriteString(c.Delta.Content)
			for _, tc := range c.Delta.ToolCalls {
				idx := 0
				if tc.Index != nil {
					idx = *tc.Index
				}
				i, seen := at[idx]
				if !seen {
					i = len(calls)
					at[idx] = i
					calls = append(calls, chatToolCall{ID: tc.ID, Type: "function"})
					calls[i].Function.Name = tc.Function.Name
				}
				calls[i].Function.Arguments += tc.Function.Arguments
			}
			if c.FinishReason != "" {
				choice.FinishReason = c.FinishReason
			}
		}
		return true
	})
	if failed != nil {
		return cr, failed
	}
	if err != nil {
		return cr, errors.New("读取上游的流式回复失败")
	}
	s := text.String()
	choice.Message.Content, choice.Message.ToolCalls = &s, calls
	cr.Choices = []chatChoice{choice}
	return cr, nil
}

func chatRespToMessage(cr chatResp, model string) anthMessage {
	msg := anthMessage{
		ID: newID("msg_"), Type: "message", Role: "assistant", Model: model,
		Content: []map[string]any{}, StopReason: "end_turn",
		Usage: map[string]int{"input_tokens": 0, "output_tokens": 0},
	}
	if cr.Usage != nil {
		msg.Usage["input_tokens"], msg.Usage["output_tokens"] = cr.Usage.PromptTokens, cr.Usage.CompletionTokens
	}
	if len(cr.Choices) == 0 {
		return msg
	}
	c := cr.Choices[0]
	if c.Message.Content != nil && *c.Message.Content != "" {
		msg.Content = append(msg.Content, map[string]any{"type": "text", "text": *c.Message.Content})
	}
	for _, tc := range c.Message.ToolCalls {
		id := tc.ID
		if id == "" {
			id = newID("toolu_")
		}
		msg.Content = append(msg.Content, map[string]any{"type": "tool_use", "id": id, "name": tc.Function.Name, "input": parseArgs(tc.Function.Arguments)})
	}
	msg.StopReason = stopReason(c.FinishReason)
	if len(c.Message.ToolCalls) > 0 && msg.StopReason == "end_turn" {
		msg.StopReason = "tool_use"
	}
	return msg
}

func parseArgs(s string) any {
	var v any
	if strings.TrimSpace(s) == "" || json.Unmarshal([]byte(s), &v) != nil {
		return map[string]any{}
	}
	return v
}

// chatToAnthropic writes an OpenAI Chat upstream's answer back to the agent
// as an Anthropic Messages answer, streaming or not as the agent asked.
func chatToAnthropic(w http.ResponseWriter, resp *http.Response, stream bool, model string) {
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		dialectAnthropic.writeErr(w, resp.StatusCode, upstreamMessage(b, resp.StatusCode))
		return
	}
	var msg anthMessage
	if strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		if stream {
			streamChatAsAnthropic(w, resp.Body, model)
			return
		}
		cr, err := collectChatStream(resp.Body)
		if err != nil {
			dialectAnthropic.writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
		msg = chatRespToMessage(cr, model)
	} else {
		b, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
		if err != nil {
			dialectAnthropic.writeErr(w, http.StatusBadGateway, "读取上游回复失败")
			return
		}
		if msg, err = chatJSONToMessage(b, model); err != nil {
			dialectAnthropic.writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
	}
	if stream {
		// The agent asked for a stream but the upstream answered at once:
		// replay the whole answer as the events the agent expects.
		replayAsStream(newSSE(w), msg)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(msg)
}

// upstreamMessage pulls the human-readable part out of an error body.
func upstreamMessage(b []byte, status int) string {
	var e struct {
		Error any `json:"error"`
	}
	if json.Unmarshal(b, &e) == nil {
		switch v := e.Error.(type) {
		case string:
			return "上游：" + v
		case map[string]any:
			if m, _ := v["message"].(string); m != "" {
				return "上游：" + m
			}
		}
	}
	text := strings.TrimSpace(string(b))
	if r := []rune(text); len(r) > 300 {
		text = string(r[:300]) + "…"
	}
	if text == "" {
		return fmt.Sprintf("上游返回 HTTP %d", status)
	}
	return "上游：" + text
}

type sse struct {
	w http.ResponseWriter
	f http.Flusher
}

func newSSE(w http.ResponseWriter) *sse {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	f, _ := w.(http.Flusher)
	return &sse{w: w, f: f}
}

func (s *sse) event(name string, v any) {
	b, _ := json.Marshal(v)
	fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", name, b)
	if s.f != nil {
		s.f.Flush()
	}
}

func messageStart(model string) map[string]any {
	return map[string]any{"type": "message_start", "message": map[string]any{
		"id": newID("msg_"), "type": "message", "role": "assistant", "model": model,
		"content": []any{}, "stop_reason": nil, "stop_sequence": nil,
		"usage": map[string]int{"input_tokens": 0, "output_tokens": 0},
	}}
}

func replayAsStream(out *sse, msg anthMessage) {
	start := messageStart(msg.Model)
	start["message"].(map[string]any)["id"] = msg.ID
	out.event("message_start", start)
	for i, blk := range msg.Content {
		switch blk["type"] {
		case "text":
			out.event("content_block_start", map[string]any{"type": "content_block_start", "index": i, "content_block": map[string]any{"type": "text", "text": ""}})
			out.event("content_block_delta", map[string]any{"type": "content_block_delta", "index": i, "delta": map[string]any{"type": "text_delta", "text": blk["text"]}})
		case "tool_use":
			out.event("content_block_start", map[string]any{"type": "content_block_start", "index": i, "content_block": map[string]any{"type": "tool_use", "id": blk["id"], "name": blk["name"], "input": map[string]any{}}})
			args, _ := json.Marshal(blk["input"])
			out.event("content_block_delta", map[string]any{"type": "content_block_delta", "index": i, "delta": map[string]any{"type": "input_json_delta", "partial_json": string(args)}})
		}
		out.event("content_block_stop", map[string]any{"type": "content_block_stop", "index": i})
	}
	out.event("message_delta", map[string]any{"type": "message_delta",
		"delta": map[string]any{"stop_reason": msg.StopReason, "stop_sequence": nil}, "usage": msg.Usage})
	out.event("message_stop", map[string]any{"type": "message_stop"})
}

// Chat Completions streaming chunk.
type chatChunk struct {
	Choices []struct {
		Delta struct {
			Content   string         `json:"content"`
			ToolCalls []chatToolCall `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *chatUsage `json:"usage"`
	Error any        `json:"error"`
}

// streamState turns Chat deltas into Anthropic content blocks. Anthropic
// blocks are sequential, so a new kind of delta closes the open block.
type streamState struct {
	out      *sse
	next     int
	open     int
	openKind string
	tools    map[int]int // Chat tool_call index -> Anthropic block index
	stop     string
	usedTool bool
	in, outT int
}

func (st *streamState) closeOpen() {
	if st.open >= 0 {
		st.out.event("content_block_stop", map[string]any{"type": "content_block_stop", "index": st.open})
		st.open, st.openKind = -1, ""
	}
}

func (st *streamState) text(s string) {
	if st.openKind != "text" {
		st.closeOpen()
		st.open, st.openKind = st.next, "text"
		st.next++
		st.out.event("content_block_start", map[string]any{"type": "content_block_start", "index": st.open, "content_block": map[string]any{"type": "text", "text": ""}})
	}
	st.out.event("content_block_delta", map[string]any{"type": "content_block_delta", "index": st.open, "delta": map[string]any{"type": "text_delta", "text": s}})
}

func (st *streamState) tool(tc chatToolCall) {
	idx := 0
	if tc.Index != nil {
		idx = *tc.Index
	}
	blk, seen := st.tools[idx]
	if !seen {
		st.closeOpen()
		id := tc.ID
		if id == "" {
			id = newID("toolu_")
		}
		blk = st.next
		st.next++
		st.tools[idx] = blk
		st.open, st.openKind, st.usedTool = blk, "tool", true
		st.out.event("content_block_start", map[string]any{"type": "content_block_start", "index": blk,
			"content_block": map[string]any{"type": "tool_use", "id": id, "name": tc.Function.Name, "input": map[string]any{}}})
	}
	if tc.Function.Arguments != "" {
		st.out.event("content_block_delta", map[string]any{"type": "content_block_delta", "index": blk,
			"delta": map[string]any{"type": "input_json_delta", "partial_json": tc.Function.Arguments}})
	}
}

func (st *streamState) finish() {
	st.closeOpen()
	stop := st.stop
	if stop == "" || (st.usedTool && stop == "end_turn") {
		if st.usedTool {
			stop = "tool_use"
		} else {
			stop = "end_turn"
		}
	}
	st.out.event("message_delta", map[string]any{"type": "message_delta",
		"delta": map[string]any{"stop_reason": stop, "stop_sequence": nil},
		"usage": map[string]int{"input_tokens": st.in, "output_tokens": st.outT}})
	st.out.event("message_stop", map[string]any{"type": "message_stop"})
}

// chatChunks calls fn for each chunk of a Chat Completions SSE body until
// [DONE], the end of the body, or fn returning false.
func chatChunks(body io.Reader, fn func(chatChunk) bool) error {
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			return nil
		}
		var ch chatChunk
		if json.Unmarshal([]byte(data), &ch) != nil {
			continue
		}
		if !fn(ch) {
			return nil
		}
	}
	return sc.Err()
}

func streamChatAsAnthropic(w http.ResponseWriter, body io.Reader, model string) {
	st := &streamState{out: newSSE(w), open: -1, tools: map[int]int{}}
	st.out.event("message_start", messageStart(model))
	failed := false
	_ = chatChunks(body, func(ch chatChunk) bool {
		if ch.Error != nil {
			b, _ := json.Marshal(map[string]any{"error": ch.Error})
			st.closeOpen()
			st.out.event("error", map[string]any{"type": "error", "error": map[string]any{"type": "api_error", "message": upstreamMessage(b, 0)}})
			failed = true
			return false
		}
		if ch.Usage != nil {
			st.in, st.outT = ch.Usage.PromptTokens, ch.Usage.CompletionTokens
		}
		for _, c := range ch.Choices {
			if c.Delta.Content != "" {
				st.text(c.Delta.Content)
			}
			for _, tc := range c.Delta.ToolCalls {
				st.tool(tc)
			}
			if c.FinishReason != "" {
				st.stop = stopReason(c.FinishReason)
			}
		}
		return true
	})
	if !failed {
		st.finish()
	}
}
