package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nearmetips/DailiCode/internal/core"
)

const localToken = "gw-test-token"

// upstream is a fake OpenAI-compatible provider that records what it was sent.
type upstream struct {
	*httptest.Server
	hits    atomic.Int32
	mu      sync.Mutex
	lastKey string
	lastBod string
	lastUA  string
	lastHdr http.Header
	lastURL string
}

func newUpstream(t *testing.T, h func(w http.ResponseWriter, r *http.Request)) *upstream {
	t.Helper()
	u := &upstream{}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.hits.Add(1)
		b, _ := io.ReadAll(r.Body)
		u.mu.Lock()
		u.lastKey, u.lastBod, u.lastUA = r.Header.Get("Authorization"), string(b), r.Header.Get("User-Agent")
		u.lastHdr, u.lastURL = r.Header.Clone(), r.URL.RequestURI()
		u.mu.Unlock()
		h(w, r)
	}))
	t.Cleanup(u.Close)
	return u
}

func status(code int) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		io.WriteString(w, `{"error":{"message":"upstream said no"}}`)
	}
}

func ok(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, `{"id":"chatcmpl-1","choices":[{"message":{"role":"assistant","content":"hi"}}]}`)
}

type rig struct {
	srv  *Server
	ts   *httptest.Server
	cfg  *core.Gateway
	mu   sync.Mutex
	nowT time.Time
}

func newRig(t *testing.T, providers ...core.GatewayProvider) *rig {
	t.Helper()
	r := &rig{cfg: &core.Gateway{Token: localToken, Providers: providers}, nowT: time.Now()}
	r.srv = New(func() (core.Gateway, error) {
		r.mu.Lock()
		defer r.mu.Unlock()
		return *r.cfg, nil
	})
	r.srv.now = func() time.Time {
		r.mu.Lock()
		defer r.mu.Unlock()
		return r.nowT
	}
	r.ts = httptest.NewServer(r.srv.Handler())
	t.Cleanup(r.ts.Close)
	return r
}

func (r *rig) advance(d time.Duration) {
	r.mu.Lock()
	r.nowT = r.nowT.Add(d)
	r.mu.Unlock()
}

func prov(name string, u *upstream, key string) core.GatewayProvider {
	return core.GatewayProvider{Name: name, BaseURL: u.URL + "/v1", APIKey: key}
}

func (r *rig) chat(t *testing.T, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, r.ts.URL+"/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+localToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func read(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

const reqBody = `{"model":"gpt-x","messages":[{"role":"user","content":"hello"}]}`

func TestFailsOverOn429AndParksTheProvider(t *testing.T) {
	a := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "120")
		status(429)(w, r)
	})
	b := newUpstream(t, ok)
	g := newRig(t, prov("a", a, "key-a"), prov("b", b, "key-b"))

	resp := g.chat(t, reqBody)
	if resp.StatusCode != 200 || !strings.Contains(read(t, resp), "chatcmpl-1") {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if a.hits.Load() != 1 || b.hits.Load() != 1 {
		t.Fatalf("hits a=%d b=%d", a.hits.Load(), b.hits.Load())
	}

	// The next request skips the parked provider entirely.
	read(t, g.chat(t, reqBody))
	if a.hits.Load() != 1 || b.hits.Load() != 2 {
		t.Fatalf("after cooldown skip: a=%d b=%d", a.hits.Load(), b.hits.Load())
	}

	// Once Retry-After has passed, the first provider gets traffic again.
	g.advance(121 * time.Second)
	read(t, g.chat(t, reqBody))
	if a.hits.Load() != 2 {
		t.Fatalf("provider a not retried after its cooldown: %d", a.hits.Load())
	}
}

func TestDoesNotFailOverOnRequestErrors(t *testing.T) {
	a := newUpstream(t, status(400))
	b := newUpstream(t, ok)
	g := newRig(t, prov("a", a, "key-a"), prov("b", b, "key-b"))

	resp := g.chat(t, reqBody)
	body := read(t, resp)
	if resp.StatusCode != 400 || !strings.Contains(body, "upstream said no") {
		t.Fatalf("a 400 must reach the agent unchanged: %d %s", resp.StatusCode, body)
	}
	if b.hits.Load() != 0 {
		t.Fatal("a bad request was retried on another provider")
	}
	// And the provider is not parked for it.
	read(t, g.chat(t, reqBody))
	if a.hits.Load() != 2 {
		t.Fatalf("provider parked after a 400: %d", a.hits.Load())
	}
}

func TestFailsOverOnAuthPaymentAndServerErrors(t *testing.T) {
	for _, code := range []int{401, 402, 403, 500, 502, 503} {
		a := newUpstream(t, status(code))
		b := newUpstream(t, ok)
		g := newRig(t, prov("a", a, "key-a"), prov("b", b, "key-b"))
		resp := g.chat(t, reqBody)
		if resp.StatusCode != 200 {
			t.Fatalf("HTTP %d from the first provider: agent got %d", code, resp.StatusCode)
		}
		read(t, resp)
		if b.hits.Load() != 1 {
			t.Fatalf("HTTP %d: second provider not used", code)
		}
	}
}

func TestFailsOverWhenAProviderIsUnreachable(t *testing.T) {
	dead := newUpstream(t, ok)
	deadURL := dead.URL
	dead.Close()
	b := newUpstream(t, ok)
	g := newRig(t,
		core.GatewayProvider{Name: "dead", BaseURL: deadURL + "/v1", APIKey: "k"},
		prov("b", b, "key-b"))
	resp := g.chat(t, reqBody)
	if resp.StatusCode != 200 || b.hits.Load() != 1 {
		t.Fatalf("status %d, b hits %d", resp.StatusCode, b.hits.Load())
	}
	read(t, resp)
}

func TestQuotaStyle429ParksForLong(t *testing.T) {
	a := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		io.WriteString(w, `{"error":{"code":"insufficient_quota","message":"You exceeded your current quota"}}`)
	})
	b := newUpstream(t, ok)
	g := newRig(t, prov("a", a, "key-a"), prov("b", b, "key-b"))
	read(t, g.chat(t, reqBody))

	g.advance(10 * time.Minute) // a plain rate limit would be over by now
	read(t, g.chat(t, reqBody))
	if a.hits.Load() != 1 {
		t.Fatalf("a spent quota was retried after only 10 minutes: %d", a.hits.Load())
	}
	g.advance(25 * time.Minute)
	read(t, g.chat(t, reqBody))
	if a.hits.Load() != 2 {
		t.Fatalf("provider not retried after the quota cooldown: %d", a.hits.Load())
	}
}

func TestAllProvidersFailedIs503WithRetryAfter(t *testing.T) {
	a := newUpstream(t, status(429))
	b := newUpstream(t, status(500))
	g := newRig(t, prov("a", a, "key-a"), prov("b", b, "key-b"))
	resp := g.chat(t, reqBody)
	body := read(t, resp)
	if resp.StatusCode != 503 || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("status %d retry-after %q", resp.StatusCode, resp.Header.Get("Retry-After"))
	}
	if !strings.Contains(body, "a：") || !strings.Contains(body, "b：") || strings.Contains(body, "key-a") {
		t.Fatalf("body should name each attempt and never a key: %s", body)
	}

	// Every provider parked: refused without touching any upstream.
	before := a.hits.Load() + b.hits.Load()
	resp = g.chat(t, reqBody)
	read(t, resp)
	if resp.StatusCode != 503 || a.hits.Load()+b.hits.Load() != before {
		t.Fatal("parked providers were contacted")
	}
}

func TestNoProviderConfigured(t *testing.T) {
	g := newRig(t)
	resp := g.chat(t, reqBody)
	body := read(t, resp)
	if resp.StatusCode != 503 || !strings.Contains(body, "没有能处理这种请求的上游") || !strings.Contains(body, "<名称>") {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
}

func TestUpstreamNeverSeesTheLocalToken(t *testing.T) {
	a := newUpstream(t, ok)
	g := newRig(t, prov("a", a, "sk-upstream-secret"))
	read(t, g.chat(t, reqBody))
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.lastKey != "Bearer sk-upstream-secret" {
		t.Fatalf("upstream Authorization = %q", a.lastKey)
	}
	if strings.Contains(a.lastKey, localToken) {
		t.Fatal("local token leaked upstream")
	}
	if !strings.HasPrefix(a.lastUA, "gcccode-gateway/") {
		t.Fatalf("user agent %q", a.lastUA)
	}
}

func TestModelOverrideRewritesTheBody(t *testing.T) {
	a := newUpstream(t, ok)
	p := prov("a", a, "k")
	p.Model = "deepseek-chat"
	g := newRig(t, p)
	read(t, g.chat(t, `{"model":"gpt-x","stream":false,"messages":[{"role":"user","content":"hi"}]}`))
	a.mu.Lock()
	defer a.mu.Unlock()
	var got map[string]any
	if err := json.Unmarshal([]byte(a.lastBod), &got); err != nil {
		t.Fatal(err)
	}
	if got["model"] != "deepseek-chat" || got["stream"] != false || got["messages"] == nil {
		t.Fatalf("body %s", a.lastBod)
	}
}

func TestStreamingPassesThroughAsItArrives(t *testing.T) {
	gate := make(chan struct{})
	a := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		io.WriteString(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		<-gate
		io.WriteString(w, "data: second\n\ndata: [DONE]\n\n")
	})
	g := newRig(t, prov("a", a, "k"))
	resp := g.chat(t, `{"model":"m","stream":true,"messages":[]}`)
	defer resp.Body.Close()
	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("content-type %q", resp.Header.Get("Content-Type"))
	}
	buf := make([]byte, 64)
	n, err := resp.Body.Read(buf)
	if err != nil || !strings.Contains(string(buf[:n]), "first") {
		t.Fatalf("first event did not arrive before the stream ended: %q %v", buf[:n], err)
	}
	close(gate)
	rest, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(rest), "[DONE]") {
		t.Fatalf("rest %q", rest)
	}
}

func TestGuardRejectsMissingTokenAndForeignHost(t *testing.T) {
	a := newUpstream(t, ok)
	g := newRig(t, prov("a", a, "k"))

	for _, auth := range []string{"", "Bearer wrong"} {
		req, _ := http.NewRequest(http.MethodPost, g.ts.URL+"/v1/chat/completions", strings.NewReader(reqBody))
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, _ := http.DefaultClient.Do(req)
		read(t, resp)
		if resp.StatusCode != 401 {
			t.Fatalf("auth %q: status %d", auth, resp.StatusCode)
		}
	}

	req, _ := http.NewRequest(http.MethodPost, g.ts.URL+"/v1/chat/completions", strings.NewReader(reqBody))
	req.Host = "evil.example.com"
	req.Header.Set("Authorization", "Bearer "+localToken)
	resp, _ := http.DefaultClient.Do(req)
	read(t, resp)
	if resp.StatusCode != 403 {
		t.Fatalf("foreign Host: status %d", resp.StatusCode)
	}
	if a.hits.Load() != 0 {
		t.Fatal("a rejected request reached an upstream")
	}
}

func TestEmptyLocalTokenRefusesEverything(t *testing.T) {
	a := newUpstream(t, ok)
	g := newRig(t, prov("a", a, "k"))
	g.mu.Lock()
	g.cfg.Token = ""
	g.mu.Unlock()
	req, _ := http.NewRequest(http.MethodPost, g.ts.URL+"/v1/chat/completions", strings.NewReader(reqBody))
	resp, _ := http.DefaultClient.Do(req)
	read(t, resp)
	if resp.StatusCode != 401 {
		t.Fatalf("a gateway with no token must not be open: %d", resp.StatusCode)
	}
}

func TestBadJSONIs400AndDisabledProviderIsSkipped(t *testing.T) {
	a := newUpstream(t, ok)
	b := newUpstream(t, ok)
	pa := prov("a", a, "k")
	pa.Disabled = true
	g := newRig(t, pa, prov("b", b, "k"))
	if resp := g.chat(t, `not json`); resp.StatusCode != 400 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	read(t, g.chat(t, reqBody))
	if a.hits.Load() != 0 || b.hits.Load() != 1 {
		t.Fatalf("a=%d b=%d", a.hits.Load(), b.hits.Load())
	}
}

func TestModelsFailsOverToo(t *testing.T) {
	a := newUpstream(t, status(500))
	b := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/models" {
			t.Errorf("upstream got %s %s", r.Method, r.URL.Path)
		}
		io.WriteString(w, `{"object":"list","data":[{"id":"m1"}]}`)
	})
	g := newRig(t, prov("a", a, "k"), prov("b", b, "k"))
	req, _ := http.NewRequest(http.MethodGet, g.ts.URL+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+localToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if body := read(t, resp); resp.StatusCode != 200 || !strings.Contains(body, "m1") {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
}

func TestStatusNeverContainsAPIKeys(t *testing.T) {
	a := newUpstream(t, status(429))
	b := newUpstream(t, ok)
	g := newRig(t, prov("a", a, "sk-secret-a"), prov("b", b, "sk-secret-b"))
	read(t, g.chat(t, reqBody))

	req, _ := http.NewRequest(http.MethodGet, g.ts.URL+"/_gcccode/status", nil)
	req.Header.Set("Authorization", "Bearer "+localToken)
	resp, _ := http.DefaultClient.Do(req)
	body := read(t, resp)
	if strings.Contains(body, "sk-secret") || strings.Contains(body, localToken) {
		t.Fatalf("status leaks a secret: %s", body)
	}
	var rep StatusReport
	if err := json.Unmarshal([]byte(body), &rep); err != nil || len(rep.Providers) != 2 {
		t.Fatalf("%v %s", err, body)
	}
	if rep.Providers[0].DownUntil == 0 || rep.Providers[0].Failed != 1 || rep.Providers[1].Served != 1 {
		t.Fatalf("%+v", rep.Providers)
	}
}

func TestListenLoopbackRefusesOtherAddresses(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:0", "192.168.1.5:3426", "example.com:3426", "nonsense"} {
		if ln, err := ListenLoopback(addr); err == nil {
			ln.Close()
			t.Fatalf("%s was accepted", addr)
		}
	}
	ln, err := ListenLoopback("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln.Close()
}

func TestValidateProvider(t *testing.T) {
	good := core.GatewayProvider{Name: "a", BaseURL: "https://api.example.com/v1", APIKey: "k"}
	for _, ok := range []core.GatewayProvider{
		good,
		{Name: "a", BaseURL: "https://api.example.com/v1", APIKey: "k", Responses: true},
		{Name: "a", BaseURL: "https://api.anthropic.com", APIKey: "k", Protocol: ProtoAnthropic},
		{Name: "a", BaseURL: "https://relay.example.com/claude/", APIKey: "k", Protocol: ProtoAnthropic},
		{Name: "a", BaseURL: "https://generativelanguage.googleapis.com", APIKey: "k", Protocol: ProtoGemini},
	} {
		if err := ValidateProvider(ok); err != nil {
			t.Fatalf("%+v: %v", ok, err)
		}
	}
	for _, bad := range []core.GatewayProvider{
		{BaseURL: good.BaseURL, APIKey: "k"},
		{Name: "a", BaseURL: "api.example.com/v1", APIKey: "k"},
		{Name: "a", BaseURL: "ftp://x/v1", APIKey: "k"},
		{Name: "a", BaseURL: good.BaseURL},
		{Name: "a", BaseURL: good.BaseURL, APIKey: "k", Protocol: "azure"},
		{Name: "a", BaseURL: "https://api.anthropic.com", APIKey: "k", Protocol: ProtoAnthropic, Responses: true},
		{Name: "a", BaseURL: "https://api.anthropic.com/v1/", APIKey: "k", Protocol: ProtoAnthropic},
		{Name: "a", BaseURL: "https://generativelanguage.googleapis.com/v1beta", APIKey: "k", Protocol: ProtoGemini},
	} {
		if ValidateProvider(bad) == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Now()
	if d := parseRetryAfter("90", now); d != 90*time.Second {
		t.Fatalf("%v", d)
	}
	if d := parseRetryAfter(now.Add(2*time.Minute).UTC().Format(http.TimeFormat), now); d < time.Minute || d > 3*time.Minute {
		t.Fatalf("%v", d)
	}
	if parseRetryAfter("", now) != 0 || parseRetryAfter("soon", now) != 0 {
		t.Fatal("garbage should be 0")
	}
}
