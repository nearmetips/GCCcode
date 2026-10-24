package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAnthropicToChatRequest(t *testing.T) {
	in := `{
	  "model": "claude-sonnet-4-5", "max_tokens": 512, "temperature": 0.2, "stream": true,
	  "stop_sequences": ["END"],
	  "system": [{"type":"text","text":"你是助手"},{"type":"text","text":"简洁"}],
	  "messages": [
	    {"role":"user","content":[{"type":"text","text":"看图"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}}]},
	    {"role":"assistant","content":[{"type":"thinking","thinking":"…","signature":"s"},{"type":"text","text":"我查一下"},{"type":"tool_use","id":"toolu_1","name":"read","input":{"path":"a.go"}}]},
	    {"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":[{"type":"text","text":"package a"}]},{"type":"tool_result","tool_use_id":"toolu_2","content":"boom","is_error":true},{"type":"text","text":"继续"}]}
	  ],
	  "tools": [
	    {"name":"read","description":"读文件","input_schema":{"type":"object","properties":{"path":{"type":"string"}}}},
	    {"type":"web_search_20250305","name":"web_search"}
	  ],
	  "tool_choice": {"type":"any"}
	}`
	out, stream, model, err := anthropicToChat([]byte(in), "deepseek-chat")
	if err != nil {
		t.Fatal(err)
	}
	if !stream || model != "claude-sonnet-4-5" {
		t.Fatalf("stream=%v model=%s", stream, model)
	}
	var cr struct {
		Model         string  `json:"model"`
		MaxTokens     int     `json:"max_tokens"`
		Temperature   float64 `json:"temperature"`
		Stop          []string
		StreamOptions struct {
			IncludeUsage bool `json:"include_usage"`
		} `json:"stream_options"`
		Messages []struct {
			Role       string          `json:"role"`
			Content    json.RawMessage `json:"content"`
			ToolCallID string          `json:"tool_call_id"`
			ToolCalls  []struct {
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"messages"`
		Tools []struct {
			Function struct {
				Name       string          `json:"name"`
				Parameters json.RawMessage `json:"parameters"`
			} `json:"function"`
		} `json:"tools"`
		ToolChoice any `json:"tool_choice"`
	}
	if err := json.Unmarshal(out, &cr); err != nil {
		t.Fatal(err)
	}
	if cr.Model != "deepseek-chat" || cr.MaxTokens != 512 || cr.Temperature != 0.2 || len(cr.Stop) != 1 || !cr.StreamOptions.IncludeUsage {
		t.Fatalf("scalars: %s", out)
	}
	roles := []string{}
	for _, m := range cr.Messages {
		roles = append(roles, m.Role)
	}
	if strings.Join(roles, ",") != "system,user,assistant,tool,tool,user" {
		t.Fatalf("roles %v\n%s", roles, out)
	}
	if string(cr.Messages[0].Content) != `"你是助手\n\n简洁"` {
		t.Fatalf("system %s", cr.Messages[0].Content)
	}
	if !strings.Contains(string(cr.Messages[1].Content), `"image_url":{"url":"data:image/png;base64,AAAA"}`) {
		t.Fatalf("image %s", cr.Messages[1].Content)
	}
	asst := cr.Messages[2]
	if string(asst.Content) != `"我查一下"` || len(asst.ToolCalls) != 1 || asst.ToolCalls[0].ID != "toolu_1" ||
		asst.ToolCalls[0].Function.Arguments != `{"path":"a.go"}` {
		t.Fatalf("assistant %+v", asst)
	}
	if strings.Contains(string(out), "thinking") {
		t.Fatal("thinking block leaked into the Chat request")
	}
	if cr.Messages[3].ToolCallID != "toolu_1" || string(cr.Messages[3].Content) != `"package a"` {
		t.Fatalf("tool result %+v", cr.Messages[3])
	}
	if string(cr.Messages[4].Content) != `"Error: boom"` {
		t.Fatalf("error tool result %s", cr.Messages[4].Content)
	}
	if string(cr.Messages[5].Content) != `"继续"` {
		t.Fatalf("trailing user text %s", cr.Messages[5].Content)
	}
	if len(cr.Tools) != 1 || cr.Tools[0].Function.Name != "read" || !strings.Contains(string(cr.Tools[0].Function.Parameters), "path") {
		t.Fatalf("tools %+v", cr.Tools)
	}
	if cr.ToolChoice != "required" {
		t.Fatalf("tool_choice %v", cr.ToolChoice)
	}
}

func TestAnthropicToChatKeepsModelWithoutOverrideAndRejectsGarbage(t *testing.T) {
	out, stream, _, err := anthropicToChat([]byte(`{"model":"claude-x","max_tokens":5,"system":"s","messages":[{"role":"user","content":"hi"}]}`), "")
	if err != nil || stream {
		t.Fatal(err, stream)
	}
	if !strings.Contains(string(out), `"model":"claude-x"`) || strings.Contains(string(out), "stream_options") {
		t.Fatalf("%s", out)
	}
	if _, _, _, err := anthropicToChat([]byte(`[1,2]`), ""); err == nil {
		t.Fatal("garbage accepted")
	}
	if _, _, _, err := anthropicToChat([]byte(`{"messages":[{"role":"user","content":42}]}`), ""); err == nil {
		t.Fatal("numeric content accepted")
	}
}

func TestChatJSONToMessage(t *testing.T) {
	b := `{"choices":[{"message":{"content":"好的","tool_calls":[{"id":"call_1","type":"function","function":{"name":"read","arguments":"{\"path\":\"x\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":3}}`
	msg, err := chatJSONToMessage([]byte(b), "claude-x")
	if err != nil {
		t.Fatal(err)
	}
	if msg.StopReason != "tool_use" || msg.Model != "claude-x" || !strings.HasPrefix(msg.ID, "msg_") || len(msg.Content) != 2 {
		t.Fatalf("%+v", msg)
	}
	tool := msg.Content[1]
	if tool["type"] != "tool_use" || tool["id"] != "call_1" || tool["name"] != "read" || tool["input"].(map[string]any)["path"] != "x" {
		t.Fatalf("tool block %+v", tool)
	}
	if msg.Usage["input_tokens"] != 10 || msg.Usage["output_tokens"] != 3 {
		t.Fatalf("usage %v", msg.Usage)
	}
	for finish, want := range map[string]string{"stop": "end_turn", "length": "max_tokens", "tool_calls": "tool_use", "": "end_turn"} {
		if got := stopReason(finish); got != want {
			t.Fatalf("%q => %s", finish, got)
		}
	}
}

type event struct {
	Name string
	Data map[string]any
}

func parseEvents(t *testing.T, body string) []event {
	t.Helper()
	var out []event
	for _, chunk := range strings.Split(body, "\n\n") {
		var ev event
		for _, line := range strings.Split(chunk, "\n") {
			switch {
			case strings.HasPrefix(line, "event: "):
				ev.Name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev.Data); err != nil {
					t.Fatalf("bad data %q", line)
				}
			}
		}
		if ev.Name != "" {
			out = append(out, ev)
		}
	}
	return out
}

func TestStreamChatAsAnthropicWithTextAndTools(t *testing.T) {
	upstream := strings.Join([]string{
		`data: {"choices":[{"delta":{"role":"assistant","content":"我"}}]}`,
		`data: {"choices":[{"delta":{"content":"看看"}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"read","arguments":""}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"pa"}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"th\":\"a.go\"}"}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_b","type":"function","function":{"name":"ls","arguments":"{}"}}]}}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: {"choices":[],"usage":{"prompt_tokens":20,"completion_tokens":9}}`,
		`data: [DONE]`,
	}, "\n\n") + "\n\n"
	rec := httptest.NewRecorder()
	streamChatAsAnthropic(rec, strings.NewReader(upstream), "claude-x")
	evs := parseEvents(t, rec.Body.String())

	var seq []string
	for _, e := range evs {
		seq = append(seq, e.Name)
	}
	want := "message_start,content_block_start,content_block_delta,content_block_delta,content_block_stop," +
		"content_block_start,content_block_delta,content_block_delta,content_block_stop," +
		"content_block_start,content_block_delta,content_block_stop,message_delta,message_stop"
	if strings.Join(seq, ",") != want {
		t.Fatalf("sequence\n got %s\nwant %s", strings.Join(seq, ","), want)
	}
	if evs[0].Data["message"].(map[string]any)["model"] != "claude-x" {
		t.Fatal("model not echoed")
	}
	start := evs[5].Data
	blk := start["content_block"].(map[string]any)
	if start["index"].(float64) != 1 || blk["type"] != "tool_use" || blk["id"] != "call_a" || blk["name"] != "read" {
		t.Fatalf("tool start %+v", start)
	}
	args := evs[6].Data["delta"].(map[string]any)["partial_json"].(string) + evs[7].Data["delta"].(map[string]any)["partial_json"].(string)
	var parsed map[string]string
	if err := json.Unmarshal([]byte(args), &parsed); err != nil || parsed["path"] != "a.go" {
		t.Fatalf("tool args %q", args)
	}
	if evs[9].Data["index"].(float64) != 2 {
		t.Fatalf("second tool index %+v", evs[9].Data)
	}
	md := evs[12].Data
	if md["delta"].(map[string]any)["stop_reason"] != "tool_use" {
		t.Fatalf("a turn that called tools must stop with tool_use even when the upstream said stop: %+v", md)
	}
	if u := md["usage"].(map[string]any); u["output_tokens"].(float64) != 9 || u["input_tokens"].(float64) != 20 {
		t.Fatalf("usage %+v", md["usage"])
	}
}

func TestStreamErrorBecomesAnErrorEvent(t *testing.T) {
	upstream := `data: {"choices":[{"delta":{"content":"a"}}]}` + "\n\n" + `data: {"error":{"message":"upstream exploded"}}` + "\n\n"
	rec := httptest.NewRecorder()
	streamChatAsAnthropic(rec, strings.NewReader(upstream), "m")
	evs := parseEvents(t, rec.Body.String())
	last := evs[len(evs)-1]
	if last.Name != "error" || !strings.Contains(last.Data["error"].(map[string]any)["message"].(string), "upstream exploded") {
		t.Fatalf("%+v", evs)
	}
}

func TestStreamedAnswerIsCollectedWhenTheAgentWantedJSON(t *testing.T) {
	upstream := strings.Join([]string{
		`data: {"choices":[{"delta":{"content":"读"}}]}`,
		`data: {"choices":[{"delta":{"content":"一下"}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_9","type":"function","function":{"name":"Read","arguments":"{\"path\":"}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"a.go\"}"}}]}}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":5,"completion_tokens":3}}`,
		`data: [DONE]`,
	}, "\n\n") + "\n\n"
	resp := &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": {"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(upstream)),
	}
	rec := httptest.NewRecorder()
	chatToAnthropic(rec, resp, false, "claude-x")
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("content type %q: %s", ct, rec.Body.String())
	}
	var msg anthMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &msg); err != nil {
		t.Fatal(err)
	}
	if msg.Model != "claude-x" || msg.StopReason != "tool_use" || len(msg.Content) != 2 ||
		msg.Content[0]["text"] != "读一下" || msg.Content[1]["id"] != "call_9" ||
		msg.Content[1]["input"].(map[string]any)["path"] != "a.go" || msg.Usage["output_tokens"] != 3 {
		t.Fatalf("%+v", msg)
	}

	broken := &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": {"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(`data: {"error":{"message":"upstream exploded"}}` + "\n\n")),
	}
	rec = httptest.NewRecorder()
	chatToAnthropic(rec, broken, false, "claude-x")
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "upstream exploded") ||
		!strings.Contains(rec.Body.String(), `"type":"error"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestReplayWhenUpstreamIgnoresStream(t *testing.T) {
	msg, err := chatJSONToMessage([]byte(`{"choices":[{"message":{"content":"整段"},"finish_reason":"length"}]}`), "m")
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	replayAsStream(newSSE(rec), msg)
	evs := parseEvents(t, rec.Body.String())
	if len(evs) != 6 || evs[2].Data["delta"].(map[string]any)["text"] != "整段" ||
		evs[4].Data["delta"].(map[string]any)["stop_reason"] != "max_tokens" {
		t.Fatalf("%+v", evs)
	}
}
