package gateway

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/nearmetips/DailiCode/internal/core"
)

// This file lets agents that do not speak the OpenAI Responses API still ride
// a pooled Codex (ChatGPT) subscription: an incoming Chat Completions or
// Anthropic Messages request is translated into a Responses request, sent to
// the Codex pool, and the Responses answer translated back into the shape the
// agent asked for. The gateway uses its own identity here; it does not pretend
// to be the Codex client, so these are plainly gateway requests.

// Responses request.
type respReq struct {
	Model           string     `json:"model"`
	Instructions    string     `json:"instructions,omitempty"`
	Input           []respItem `json:"input"`
	Stream          bool       `json:"stream"`
	Tools           []respTool `json:"tools,omitempty"`
	ToolChoice      any        `json:"tool_choice,omitempty"`
	MaxOutputTokens int        `json:"max_output_tokens,omitempty"`
	Temperature     *float64   `json:"temperature,omitempty"`
	TopP            *float64   `json:"top_p,omitempty"`
	Store           bool       `json:"store"`
	PromptCacheKey  string     `json:"prompt_cache_key,omitempty"`
}

type respItem struct {
	Type    string     `json:"type"`
	Role    string     `json:"role,omitempty"`
	Content []respPart `json:"content,omitempty"`
	// function_call
	CallID    string `json:"call_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
	// function_call_output
	Output string `json:"output,omitempty"`
}

type respPart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
}

type respTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// chatIn parses an incoming Chat Completions request far enough to translate.
type chatIn struct {
	Model       string      `json:"model"`
	Messages    []chatInMsg `json:"messages"`
	MaxTokens   int         `json:"max_tokens"`
	Temperature *float64    `json:"temperature"`
	TopP        *float64    `json:"top_p"`
	Stream      bool        `json:"stream"`
	Tools       []chatTool  `json:"tools"`
	ToolChoice  any         `json:"tool_choice"`
}

type chatInMsg struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	ToolCalls  []chatToolCall  `json:"tool_calls"`
	ToolCallID string          `json:"tool_call_id"`
}

// chatToResponses turns a Chat Completions request into a Responses one.
func chatToResponses(body []byte, model, promptKey string) (out []byte, stream bool, err error) {
	var in chatIn
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, false, errors.New("不是合法的 Chat Completions 请求")
	}
	rr := respReq{Model: firstModel(model, in.Model), Stream: in.Stream, MaxOutputTokens: in.MaxTokens,
		Temperature: in.Temperature, TopP: in.TopP, Store: false, PromptCacheKey: promptKey}
	var sys []string
	for _, m := range in.Messages {
		switch m.Role {
		case "system", "developer":
			sys = append(sys, chatContentText(m.Content))
		case "assistant":
			if text := chatContentText(m.Content); text != "" {
				rr.Input = append(rr.Input, respItem{Type: "message", Role: "assistant", Content: []respPart{{Type: "output_text", Text: text}}})
			}
			for _, tc := range m.ToolCalls {
				rr.Input = append(rr.Input, respItem{Type: "function_call", CallID: tc.ID, Name: tc.Function.Name, Arguments: firstModel(tc.Function.Arguments, "{}")})
			}
		case "tool":
			rr.Input = append(rr.Input, respItem{Type: "function_call_output", CallID: m.ToolCallID, Output: chatContentText(m.Content)})
		default:
			rr.Input = append(rr.Input, respItem{Type: "message", Role: "user", Content: chatParts(m.Content)})
		}
	}
	rr.Instructions = strings.Join(sys, "\n\n")
	for _, t := range in.Tools {
		if t.Type != "" && t.Type != "function" {
			continue
		}
		rr.Tools = append(rr.Tools, respTool{Type: "function", Name: t.Function.Name, Description: t.Function.Description, Parameters: t.Function.Parameters})
	}
	rr.ToolChoice = toolChoiceResp(in.ToolChoice)
	out, err = json.Marshal(rr)
	return out, in.Stream, err
}

// anthropicToResponses turns an Anthropic Messages request into a Responses one.
func anthropicToResponses(body []byte, model, promptKey string) (out []byte, stream bool, agentModel string, err error) {
	var in anthReq
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, false, "", errors.New("不是合法的 Anthropic Messages 请求")
	}
	rr := respReq{Model: firstModel(model, in.Model), Stream: in.Stream, MaxOutputTokens: in.MaxTokens,
		Temperature: in.Temperature, TopP: in.TopP, Store: false, PromptCacheKey: promptKey}
	if sys, err := anthBlocks(in.System); err == nil {
		rr.Instructions = joinText(sys)
	}
	for _, m := range in.Messages {
		bs, err := anthBlocks(m.Content)
		if err != nil {
			return nil, false, "", err
		}
		if m.Role == "assistant" {
			var text []string
			for _, b := range bs {
				switch b.Type {
				case "text":
					if b.Text != "" {
						text = append(text, b.Text)
					}
				case "tool_use":
					rr.Input = append(rr.Input, respItem{Type: "function_call", CallID: b.ID, Name: b.Name, Arguments: firstModel(string(b.Input), "{}")})
				}
			}
			if len(text) > 0 {
				rr.Input = append(rr.Input, respItem{Type: "message", Role: "assistant", Content: []respPart{{Type: "output_text", Text: strings.Join(text, "\n\n")}}})
			}
			continue
		}
		var parts []respPart
		for _, b := range bs {
			switch b.Type {
			case "text":
				if b.Text != "" {
					parts = append(parts, respPart{Type: "input_text", Text: b.Text})
				}
			case "image":
				if u := imageURL(b); u != "" {
					parts = append(parts, respPart{Type: "input_image", ImageURL: u})
				}
			case "tool_result":
				inner, _ := anthBlocks(b.Content)
				rr.Input = append(rr.Input, respItem{Type: "function_call_output", CallID: b.ToolUseID, Output: joinText(inner)})
			}
		}
		if len(parts) > 0 {
			rr.Input = append(rr.Input, respItem{Type: "message", Role: "user", Content: parts})
		}
	}
	for _, t := range in.Tools {
		if t.Type != "" && t.Type != "custom" {
			continue
		}
		params := t.InputSchema
		if len(params) == 0 {
			params = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		rr.Tools = append(rr.Tools, respTool{Type: "function", Name: t.Name, Description: t.Description, Parameters: params})
	}
	if tc := in.ToolChoice; tc != nil {
		switch tc.Type {
		case "auto":
			rr.ToolChoice = "auto"
		case "any":
			rr.ToolChoice = "required"
		case "none":
			rr.ToolChoice = "none"
		case "tool":
			rr.ToolChoice = map[string]any{"type": "function", "name": tc.Name}
		}
	}
	out, err = json.Marshal(rr)
	return out, in.Stream, in.Model, err
}

func firstModel(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func toolChoiceResp(tc any) any {
	switch v := tc.(type) {
	case string:
		return v
	case map[string]any:
		if fn, ok := v["function"].(map[string]any); ok {
			if name, _ := fn["name"].(string); name != "" {
				return map[string]any{"type": "function", "name": name}
			}
		}
	}
	return nil
}

// chatContentText flattens a Chat message content to plain text.
func chatContentText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []chatPart
	if json.Unmarshal(raw, &parts) == nil {
		var text []string
		for _, p := range parts {
			if p.Text != "" {
				text = append(text, p.Text)
			}
		}
		return strings.Join(text, "\n\n")
	}
	return ""
}

// chatParts turns a user Chat message into Responses input parts.
func chatParts(raw json.RawMessage) []respPart {
	if len(raw) == 0 {
		return nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return []respPart{{Type: "input_text", Text: s}}
	}
	var parts []chatPart
	if json.Unmarshal(raw, &parts) != nil {
		return nil
	}
	var out []respPart
	for _, p := range parts {
		switch p.Type {
		case "text":
			out = append(out, respPart{Type: "input_text", Text: p.Text})
		case "image_url":
			if p.ImageURL != nil {
				out = append(out, respPart{Type: "input_image", ImageURL: p.ImageURL.URL})
			}
		}
	}
	return out
}

// forwardChatViaCodex answers a Chat Completions request from the Codex pool.
func (s *Server) forwardChatViaCodex(w http.ResponseWriter, r *http.Request, cfg core.Gateway, p core.GatewayProvider, body []byte, fields map[string]json.RawMessage) bool {
	key := fieldString(fields, "prompt_cache_key")
	built, stream, err := chatToResponses(body, p.Model, key)
	if err != nil {
		dialectOpenAI.writeErr(w, http.StatusBadRequest, err.Error())
		return true
	}
	model := fieldString(fields, "model")
	return s.routeCodexSub(w, r, cfg, p, key, dialectOpenAI,
		func(core.Account, bool) []byte { return built },
		func(w http.ResponseWriter, resp *http.Response) { responsesToChat(w, resp, stream, model) },
		s.hasAPIFallback(cfg, apiOpenAI), "Codex")
}

// forwardMessagesViaCodex answers an Anthropic Messages request (from an agent
// other than Claude Code) from the Codex pool.
func (s *Server) forwardMessagesViaCodex(w http.ResponseWriter, r *http.Request, cfg core.Gateway, p core.GatewayProvider, body []byte) bool {
	built, stream, model, err := anthropicToResponses(body, p.Model, "")
	if err != nil {
		dialectAnthropic.writeErr(w, http.StatusBadRequest, err.Error())
		return true
	}
	return s.routeCodexSub(w, r, cfg, p, "", dialectAnthropic,
		func(core.Account, bool) []byte { return built },
		func(w http.ResponseWriter, resp *http.Response) { responsesToAnthropic(w, resp, stream, model) },
		s.hasAPIFallback(cfg, apiMessages), "Codex")
}

// Responses output, collected from the stream or the JSON body.
type respOut struct {
	text   strings.Builder
	calls  []respCall
	inTok  int
	outTok int
	stop   string
}

type respCall struct {
	id   string
	name string
	args strings.Builder
}

// respEvent is one Responses SSE data object or JSON body fragment.
type respEvent struct {
	Type     string          `json:"type"`
	Delta    string          `json:"delta"`
	Item     *respOutputItem `json:"item"`
	Response *struct {
		Usage  *respUsage       `json:"usage"`
		Output []respOutputItem `json:"output"`
	} `json:"response"`
	Output []respOutputItem `json:"output"`
	Usage  *respUsage       `json:"usage"`
}

type respOutputItem struct {
	Type    string `json:"type"`
	Role    string `json:"role"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	ID        string `json:"id"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type respUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// collectResponses reads a Responses answer, streamed or whole, into one
// result. onDelta, when set, is called as text and tool arguments arrive.
func collectResponses(resp *http.Response) (respOut, error) {
	var out respOut
	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/event-stream") {
		b, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
		if err != nil {
			return out, err
		}
		var ev respEvent
		if json.Unmarshal(b, &ev) != nil {
			return out, errors.New("上游返回的不是合法 Responses JSON")
		}
		applyOutputItems(&out, ev.Output)
		if ev.Usage != nil {
			out.inTok, out.outTok = ev.Usage.InputTokens, ev.Usage.OutputTokens
		}
		return out, nil
	}
	err := responsesEvents(resp.Body, func(ev respEvent) {
		accumulate(&out, ev)
	})
	return out, err
}

func accumulate(out *respOut, ev respEvent) {
	switch ev.Type {
	case "response.output_text.delta":
		out.text.WriteString(ev.Delta)
	case "response.output_item.added":
		if ev.Item != nil && ev.Item.Type == "function_call" {
			out.calls = append(out.calls, respCall{id: firstModel(ev.Item.CallID, ev.Item.ID), name: ev.Item.Name})
		}
	case "response.function_call_arguments.delta":
		if n := len(out.calls); n > 0 {
			out.calls[n-1].args.WriteString(ev.Delta)
		}
	case "response.completed", "response.done":
		if ev.Response != nil {
			if ev.Response.Usage != nil {
				out.inTok, out.outTok = ev.Response.Usage.InputTokens, ev.Response.Usage.OutputTokens
			}
			applyOutputItems(out, ev.Response.Output)
		}
	}
}

func applyOutputItems(out *respOut, items []respOutputItem) {
	for _, it := range items {
		switch it.Type {
		case "message":
			for _, c := range it.Content {
				if c.Type == "output_text" {
					out.text.WriteString(c.Text)
				}
			}
		case "function_call":
			call := respCall{id: firstModel(it.CallID, it.ID), name: it.Name}
			call.args.WriteString(it.Arguments)
			out.calls = append(out.calls, call)
		}
	}
}

// responsesEvents calls fn for each SSE data object in a Responses stream.
func responsesEvents(body io.Reader, fn func(respEvent)) error {
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		var ev respEvent
		if json.Unmarshal([]byte(data), &ev) == nil {
			fn(ev)
		}
	}
	return sc.Err()
}

// responsesToChat writes a Responses answer back as Chat Completions.
func responsesToChat(w http.ResponseWriter, resp *http.Response, stream bool, model string) {
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		dialectOpenAI.writeErr(w, resp.StatusCode, upstreamMessage(b, resp.StatusCode))
		return
	}
	out, err := collectResponses(resp)
	if err != nil {
		dialectOpenAI.writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	cr := chatResp{Usage: &chatUsage{PromptTokens: out.inTok, CompletionTokens: out.outTok}}
	var choice chatChoice
	text := out.text.String()
	choice.Message.Content = &text
	for _, c := range out.calls {
		var tc chatToolCall
		tc.ID, tc.Type = c.id, "function"
		tc.Function.Name, tc.Function.Arguments = c.name, c.args.String()
		choice.Message.ToolCalls = append(choice.Message.ToolCalls, tc)
	}
	choice.FinishReason = "stop"
	if len(out.calls) > 0 {
		choice.FinishReason = "tool_calls"
	}
	cr.Choices = []chatChoice{choice}
	if stream {
		writeChatStream(w, cr, model)
		return
	}
	writeChatJSON(w, cr, model)
}

// responsesToAnthropic writes a Responses answer back as Anthropic Messages.
func responsesToAnthropic(w http.ResponseWriter, resp *http.Response, stream bool, model string) {
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		dialectAnthropic.writeErr(w, resp.StatusCode, upstreamMessage(b, resp.StatusCode))
		return
	}
	out, err := collectResponses(resp)
	if err != nil {
		dialectAnthropic.writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	msg := anthMessage{ID: newID("msg_"), Type: "message", Role: "assistant", Model: model,
		Content: []map[string]any{}, StopReason: "end_turn",
		Usage: map[string]int{"input_tokens": out.inTok, "output_tokens": out.outTok}}
	if t := out.text.String(); t != "" {
		msg.Content = append(msg.Content, map[string]any{"type": "text", "text": t})
	}
	for _, c := range out.calls {
		id := c.id
		if id == "" {
			id = newID("toolu_")
		}
		msg.Content = append(msg.Content, map[string]any{"type": "tool_use", "id": id, "name": c.name, "input": parseArgs(c.args.String())})
	}
	if len(out.calls) > 0 {
		msg.StopReason = "tool_use"
	}
	if stream {
		replayAsStream(newSSE(w), msg)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(msg)
}

// writeChatJSON writes a one-shot Chat Completions answer.
func writeChatJSON(w http.ResponseWriter, cr chatResp, model string) {
	w.Header().Set("Content-Type", "application/json")
	resp := map[string]any{
		"id": newID("chatcmpl-"), "object": "chat.completion", "model": model,
		"choices": []any{chatChoiceJSON(cr.Choices[0])},
	}
	if cr.Usage != nil {
		resp["usage"] = map[string]int{"prompt_tokens": cr.Usage.PromptTokens, "completion_tokens": cr.Usage.CompletionTokens, "total_tokens": cr.Usage.PromptTokens + cr.Usage.CompletionTokens}
	}
	_ = json.NewEncoder(w).Encode(resp)
}

func chatChoiceJSON(c chatChoice) map[string]any {
	msg := map[string]any{"role": "assistant"}
	if c.Message.Content != nil {
		msg["content"] = *c.Message.Content
	} else {
		msg["content"] = nil
	}
	if len(c.Message.ToolCalls) > 0 {
		var tcs []any
		for _, tc := range c.Message.ToolCalls {
			tcs = append(tcs, map[string]any{"id": tc.ID, "type": "function",
				"function": map[string]string{"name": tc.Function.Name, "arguments": tc.Function.Arguments}})
		}
		msg["tool_calls"] = tcs
	}
	return map[string]any{"index": 0, "message": msg, "finish_reason": c.FinishReason}
}

// writeChatStream replays a collected answer as a single Chat Completions SSE
// burst, which is what agents that set stream:true expect to read.
func writeChatStream(w http.ResponseWriter, cr chatResp, model string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	f, _ := w.(http.Flusher)
	id := newID("chatcmpl-")
	send := func(delta map[string]any, finish any) {
		chunk := map[string]any{"id": id, "object": "chat.completion.chunk", "model": model,
			"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}}
		b, _ := json.Marshal(chunk)
		_, _ = io.WriteString(w, "data: "+string(b)+"\n\n")
		if f != nil {
			f.Flush()
		}
	}
	c := cr.Choices[0]
	send(map[string]any{"role": "assistant"}, nil)
	if c.Message.Content != nil && *c.Message.Content != "" {
		send(map[string]any{"content": *c.Message.Content}, nil)
	}
	for i, tc := range c.Message.ToolCalls {
		send(map[string]any{"tool_calls": []any{map[string]any{"index": i, "id": tc.ID, "type": "function",
			"function": map[string]string{"name": tc.Function.Name, "arguments": tc.Function.Arguments}}}}, nil)
	}
	send(map[string]any{}, c.FinishReason)
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
	if f != nil {
		f.Flush()
	}
}
