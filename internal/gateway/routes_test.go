package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/nearmetips/DailiCode/internal/core"
)

func (r *rig) post(t *testing.T, path, body string, hdr map[string]string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, r.ts.URL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func anthProv(name string, u *upstream, key string) core.GatewayProvider {
	return core.GatewayProvider{Name: name, BaseURL: u.URL, APIKey: key, Protocol: ProtoAnthropic}
}

func geminiProv(name string, u *upstream, key string) core.GatewayProvider {
	return core.GatewayProvider{Name: name, BaseURL: u.URL, APIKey: key, Protocol: ProtoGemini}
}

const anthBody = `{"model":"claude-sonnet-4-5","max_tokens":100,"messages":[{"role":"user","content":"hello"}]}`

func TestResponsesOnlyGoesToUpstreamsThatSupportIt(t *testing.T) {
	chatOnly := newUpstream(t, ok)
	both := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Errorf("path %s", r.URL.Path)
		}
		io.WriteString(w, `{"id":"resp_1","output":[]}`)
	})
	pb := prov("both", both, "k2")
	pb.Responses = true
	g := newRig(t, prov("chat-only", chatOnly, "k1"), pb)
	resp := g.post(t, "/v1/responses", `{"model":"gpt-x","input":"hi"}`, map[string]string{"Authorization": "Bearer " + localToken})
	if body := read(t, resp); resp.StatusCode != 200 || !strings.Contains(body, "resp_1") {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if chatOnly.hits.Load() != 0 {
		t.Fatal("a chat-only upstream got a Responses request")
	}

	g2 := newRig(t, prov("chat-only", chatOnly, "k1"))
	resp = g2.post(t, "/v1/responses", `{"model":"gpt-x","input":"hi"}`, map[string]string{"Authorization": "Bearer " + localToken})
	if body := read(t, resp); resp.StatusCode != 503 || !strings.Contains(body, "没有能处理") {
		t.Fatalf("no Responses upstream: %d %s", resp.StatusCode, body)
	}
}

func TestMessagesPassThroughToAnAnthropicUpstream(t *testing.T) {
	a := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"msg_up","type":"message","content":[{"type":"text","text":"hi"}]}`)
	})
	g := newRig(t, anthProv("claude-api", a, "sk-ant-upstream"))
	resp := g.post(t, "/v1/messages?beta=true", anthBody, map[string]string{
		"X-Api-Key": localToken, "Anthropic-Version": "2023-06-01", "Anthropic-Beta": "tools-2024",
	})
	if body := read(t, resp); resp.StatusCode != 200 || !strings.Contains(body, "msg_up") {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.lastURL != "/v1/messages?beta=true" {
		t.Fatalf("upstream url %s", a.lastURL)
	}
	if a.lastHdr.Get("X-Api-Key") != "sk-ant-upstream" || a.lastHdr.Get("Authorization") != "" {
		t.Fatalf("upstream auth headers %v", a.lastHdr)
	}
	if a.lastHdr.Get("Anthropic-Beta") != "tools-2024" || a.lastHdr.Get("Anthropic-Version") != "2023-06-01" {
		t.Fatalf("anthropic headers not forwarded: %v", a.lastHdr)
	}
}

func TestMessagesTranslateToAnOpenAIUpstream(t *testing.T) {
	o := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"你好"},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":2}}`)
	})
	p := prov("deepseek", o, "sk-ds")
	p.Model = "deepseek-chat"
	g := newRig(t, p)
	resp := g.post(t, "/v1/messages", anthBody, map[string]string{"Authorization": "Bearer " + localToken})
	body := read(t, resp)
	if resp.StatusCode != 200 {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	var msg anthMessage
	if err := json.Unmarshal([]byte(body), &msg); err != nil {
		t.Fatal(err)
	}
	if msg.Type != "message" || msg.Model != "claude-sonnet-4-5" || msg.StopReason != "end_turn" ||
		len(msg.Content) != 1 || msg.Content[0]["text"] != "你好" || msg.Usage["input_tokens"] != 7 {
		t.Fatalf("%+v", msg)
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if !strings.Contains(o.lastBod, `"model":"deepseek-chat"`) || o.lastKey != "Bearer sk-ds" {
		t.Fatalf("upstream got %s / %s", o.lastBod, o.lastKey)
	}
}

func TestMessagesFailOverFromAnthropicToTranslatedOpenAI(t *testing.T) {
	a := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"Your credit balance is too low to access the Anthropic API."}}`)
	})
	o := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"choices":[{"message":{"content":"from openai"},"finish_reason":"stop"}]}`)
	})
	g := newRig(t, anthProv("claude-api", a, "k"), prov("openai", o, "k"))
	resp := g.post(t, "/v1/messages", anthBody, map[string]string{"X-Api-Key": localToken})
	if body := read(t, resp); resp.StatusCode != 200 || !strings.Contains(body, "from openai") {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if a.hits.Load() != 1 || o.hits.Load() != 1 {
		t.Fatalf("a=%d o=%d", a.hits.Load(), o.hits.Load())
	}
}

func TestCountTokensEstimatesWhileTheAnthropicUpstreamCoolsDown(t *testing.T) {
	a := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"Your credit balance is too low to access the Anthropic API."}}`)
	})
	o := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"choices":[{"message":{"content":"x"},"finish_reason":"stop"}]}`)
	})
	g := newRig(t, anthProv("claude-api", a, "k"), prov("openai", o, "k"))
	read(t, g.post(t, "/v1/messages", anthBody, map[string]string{"X-Api-Key": localToken}))
	resp := g.post(t, "/v1/messages/count_tokens", anthBody, map[string]string{"X-Api-Key": localToken})
	body := read(t, resp)
	if resp.StatusCode != 200 || !strings.Contains(body, "input_tokens") {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if a.hits.Load() != 1 {
		t.Fatalf("a cooling upstream was asked to count tokens: %d hits", a.hits.Load())
	}
}

func TestPlain400FromAnthropicIsNotRetried(t *testing.T) {
	a := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"max_tokens: field required"}}`)
	})
	o := newUpstream(t, ok)
	g := newRig(t, anthProv("claude-api", a, "k"), prov("openai", o, "k"))
	resp := g.post(t, "/v1/messages", anthBody, map[string]string{"X-Api-Key": localToken})
	if body := read(t, resp); resp.StatusCode != 400 || !strings.Contains(body, "max_tokens: field required") {
		t.Fatalf("the original 400 must reach the agent whole: %d %s", resp.StatusCode, body)
	}
	if o.hits.Load() != 0 {
		t.Fatal("a request error was retried elsewhere")
	}
}

func TestTranslatedUpstreamErrorComesBackInAnthropicShape(t *testing.T) {
	o := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		io.WriteString(w, `{"error":{"message":"Model Not Exist","type":"invalid_request_error"}}`)
	})
	g := newRig(t, prov("deepseek", o, "k"))
	resp := g.post(t, "/v1/messages", anthBody, map[string]string{"X-Api-Key": localToken})
	body := read(t, resp)
	var e struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &e); err != nil || resp.StatusCode != 400 ||
		e.Type != "error" || e.Error.Type != "invalid_request_error" || !strings.Contains(e.Error.Message, "Model Not Exist") {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
}

func TestMessagesStreamTranslatesOverHTTP(t *testing.T) {
	o := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Content-Type"), "json") {
			t.Errorf("content-type %q", r.Header.Get("Content-Type"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, `data: {"choices":[{"delta":{"content":"Hi"}}]}`+"\n\n")
		io.WriteString(w, `data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`+"\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	})
	g := newRig(t, prov("openai", o, "k"))
	resp := g.post(t, "/v1/messages", `{"model":"claude-x","max_tokens":10,"stream":true,"messages":[{"role":"user","content":"hey"}]}`,
		map[string]string{"X-Api-Key": localToken})
	body := read(t, resp)
	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("content-type %q", resp.Header.Get("Content-Type"))
	}
	names := eventNames(body)
	want := []string{"message_start", "content_block_start", "content_block_delta", "content_block_stop", "message_delta", "message_stop"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("events %v\n%s", names, body)
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if !strings.Contains(o.lastBod, `"include_usage":true`) {
		t.Fatalf("stream usage not requested: %s", o.lastBod)
	}
}

func TestCountTokensEstimatesWithoutAnAnthropicUpstream(t *testing.T) {
	o := newUpstream(t, ok)
	g := newRig(t, prov("openai", o, "k"))
	resp := g.post(t, "/v1/messages/count_tokens", anthBody, map[string]string{"X-Api-Key": localToken})
	var got map[string]int
	if err := json.Unmarshal([]byte(read(t, resp)), &got); err != nil || got["input_tokens"] <= 0 {
		t.Fatalf("%v %v", got, err)
	}
	if o.hits.Load() != 0 {
		t.Fatal("count_tokens reached an OpenAI upstream")
	}
}

func TestGeminiPassThroughStripsTheLocalKey(t *testing.T) {
	gm := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"candidates\":[]}\n\n")
	})
	p := geminiProv("gemini-api", gm, "AIza-upstream")
	p.Model = "gemini-2.5-flash"
	g := newRig(t, p)
	resp := g.post(t, "/v1beta/models/gemini-2.5-pro:streamGenerateContent?alt=sse&key="+localToken, `{"contents":[]}`, nil)
	if body := read(t, resp); resp.StatusCode != 200 || !strings.Contains(body, "candidates") {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	gm.mu.Lock()
	lastURL, lastHdr := gm.lastURL, gm.lastHdr
	gm.mu.Unlock()
	if lastURL != "/v1beta/models/gemini-2.5-flash:streamGenerateContent?alt=sse" {
		t.Fatalf("upstream url %s", lastURL)
	}
	if strings.Contains(lastURL, localToken) || lastHdr.Get("X-Goog-Api-Key") != "AIza-upstream" {
		t.Fatalf("key handling: %s %v", lastURL, lastHdr)
	}

	// The header form of the key works too.
	resp = g.post(t, "/v1beta/models/x:generateContent", `{}`, map[string]string{"X-Goog-Api-Key": localToken})
	read(t, resp)
	if resp.StatusCode != 200 {
		t.Fatalf("x-goog-api-key: %d", resp.StatusCode)
	}
}

func TestErrorsUseTheAgentsDialect(t *testing.T) {
	g := newRig(t)
	resp := g.post(t, "/v1/messages", anthBody, nil)
	body := read(t, resp)
	if resp.StatusCode != 401 || !strings.Contains(body, `"type":"error"`) || !strings.Contains(body, "authentication_error") {
		t.Fatalf("anthropic: %s", body)
	}
	resp = g.post(t, "/v1beta/models/x:generateContent", `{}`, nil)
	body = read(t, resp)
	if resp.StatusCode != 401 || !strings.Contains(body, "UNAUTHENTICATED") {
		t.Fatalf("gemini: %s", body)
	}
}

func TestGeminiModelPath(t *testing.T) {
	cases := [][3]string{
		{"/v1beta/models/gemini-2.5-pro:generateContent", "m", "/v1beta/models/m:generateContent"},
		{"/v1beta/models/gemini-2.5-pro", "m", "/v1beta/models/m"},
		{"/v1beta/models/gemini-2.5-pro:countTokens", "", "/v1beta/models/gemini-2.5-pro:countTokens"},
		{"/v1beta/files", "m", "/v1beta/files"},
	}
	for _, c := range cases {
		if got := geminiModelPath(c[0], c[1]); got != c[2] {
			t.Fatalf("%s %s => %s", c[0], c[1], got)
		}
	}
}

func eventNames(body string) []string {
	var names []string
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "event: ") {
			names = append(names, strings.TrimPrefix(line, "event: "))
		}
	}
	return names
}
