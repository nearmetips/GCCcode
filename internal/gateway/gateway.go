// Package gateway is the optional phase-2 local gateway. It accepts OpenAI
// (Chat Completions, Responses), Anthropic Messages and Gemini requests on
// loopback and forwards them to the user's own upstreams, failing over to the
// next one before the first byte reaches the agent. An upstream is either an
// API key (optionally with Messages translated to an OpenAI Chat upstream) or,
// when the user opts in, the pool's own Codex and Claude subscription logins:
// the gateway then picks an account, swaps in its credentials, rotates on
// quota, and translates between protocols so other agents can use a ChatGPT
// plan too.
package gateway

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nearmetips/DailiCode/internal/core"
	"github.com/nearmetips/DailiCode/internal/subauth"
)

// Upstream protocols.
const (
	ProtoOpenAI    = "openai"
	ProtoAnthropic = "anthropic"
	ProtoGemini    = "gemini"
)

const (
	maxBody = 32 << 20
	peekLen = 4096

	coolNetwork = 30 * time.Second
	coolServer  = 30 * time.Second
	coolRate    = 60 * time.Second // 429 with no Retry-After
	coolQuota   = 30 * time.Minute // 402, or a 429/400 that says the quota or balance is spent
	coolAuth    = time.Hour        // 401 / 403: the key itself is bad

	anthropicVersion = "2023-06-01"
)

// Protocol is p's upstream protocol, defaulting to OpenAI. A subscription
// provider's protocol follows the plan it pools: Codex speaks OpenAI
// Responses, Claude speaks Anthropic.
func Protocol(p core.GatewayProvider) string {
	switch p.Subscription {
	case SubCodex:
		return ProtoOpenAI
	case SubClaude:
		return ProtoAnthropic
	}
	if p.Protocol == "" {
		return ProtoOpenAI
	}
	return p.Protocol
}

// Loader returns the current gateway settings. It is called per request, so
// `gcccode gateway provider add` takes effect without a restart.
type Loader func() (core.Gateway, error)

type outage struct {
	Until  time.Time
	Reason string
}

type stat struct {
	Served     int
	Failed     int
	LastReason string
}

// Server is the gateway's HTTP handler plus its in-memory failover state.
type Server struct {
	load      Loader
	loadStore func() (*core.Store, error)
	client    *http.Client
	now       func() time.Time

	mu    sync.Mutex
	down  map[string]outage
	stats map[string]*stat
	// sticky remembers which pooled account a session is bound to, keyed by
	// "<kind>:<session key>", so the same conversation stays on one login.
	sticky map[string]binding
	// clients caches one HTTP client per proxy, so a subscription account
	// always leaves through its own egress.
	clients map[string]*http.Client
	// quotaAt throttles writing an account's quota back to the store.
	quotaAt map[string]time.Time

	auth *subauth.Source
}

// New builds a Server that reads its providers through load.
func New(load Loader) *Server {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	// A hung upstream must fail over instead of holding the agent forever.
	// Streaming bodies are not limited: only the wait for response headers is.
	tr.ResponseHeaderTimeout = 2 * time.Minute
	s := &Server{
		load:      load,
		loadStore: core.Load,
		client:    &http.Client{Transport: tr},
		now:       time.Now,
		down:      map[string]outage{},
		stats:     map[string]*stat{},
		sticky:    map[string]binding{},
		clients:   map[string]*http.Client{},
		quotaAt:   map[string]time.Time{},
	}
	s.auth = subauth.New(s.clientFor)
	return s
}

// Handler serves the gateway's routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if !loopbackHost(r.Host) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		_, _ = io.WriteString(w, "ok\n")
	})
	mux.HandleFunc("/v1/chat/completions", s.guard(dialectOpenAI, s.chat))
	mux.HandleFunc("/v1/responses", s.guard(dialectOpenAI, s.responses))
	mux.HandleFunc("/v1/models", s.guard(dialectOpenAI, s.models))
	mux.HandleFunc("/v1/messages", s.guard(dialectAnthropic, s.messages))
	mux.HandleFunc("/v1/messages/count_tokens", s.guard(dialectAnthropic, s.countTokens))
	mux.HandleFunc("/v1beta/", s.guard(dialectGemini, s.gemini))
	mux.HandleFunc("/_gcccode/status", s.guard(dialectOpenAI, s.status))
	return mux
}

type handlerFn func(w http.ResponseWriter, r *http.Request, cfg core.Gateway)

// guard keeps the gateway local: only loopback Host names, and only callers
// that hold the local token. A web page cannot reach it through DNS rebinding,
// and another local process cannot spend the upstream quota without the token.
func (s *Server) guard(d dialect, next handlerFn) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !loopbackHost(r.Host) {
			d.writeErr(w, http.StatusForbidden, "网关只接受本机地址")
			return
		}
		cfg, err := s.load()
		if err != nil {
			d.writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if !tokenOK(r, cfg.Token) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			d.writeErr(w, http.StatusUnauthorized, "本机网关 token 不对。运行 gcccode gateway env 取正确的值")
			return
		}
		next(w, r, cfg)
	}
}

// tokenOK accepts the local token wherever each SDK puts its key:
// Authorization: Bearer (OpenAI, Claude Code's ANTHROPIC_AUTH_TOKEN),
// x-api-key (Anthropic), x-goog-api-key or ?key= (Gemini).
func tokenOK(r *http.Request, want string) bool {
	if want == "" {
		return false
	}
	got := []string{
		strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")),
		strings.TrimSpace(r.Header.Get("X-Api-Key")),
		strings.TrimSpace(r.Header.Get("X-Goog-Api-Key")),
		r.URL.Query().Get("key"),
	}
	for _, g := range got {
		if g != "" && subtle.ConstantTimeCompare([]byte(g), []byte(want)) == 1 {
			return true
		}
	}
	return false
}

// attempt is one try against one provider.
type attempt struct {
	method string
	path   string // appended to the provider's base URL; may carry a query
	url    string // absolute URL; when set, it replaces the provider's base URL + path
	body   []byte // nil when there is none
	header http.Header
	// client, when set, sends this attempt instead of the shared one, so a
	// subscription account leaves through its own proxy.
	client *http.Client
	// auth, when set, puts the credentials on the request itself and the
	// protocol's key header and the gateway User-Agent are left alone, so a
	// pooled login reaches its real upstream unchanged.
	auth func(*http.Request)
	// commit writes the chosen upstream's answer to the agent. nil streams
	// it through unchanged.
	commit func(w http.ResponseWriter, resp *http.Response)
}

// route is how one inbound endpoint uses providers.
type route struct {
	dialect dialect
	serves  func(core.GatewayProvider) bool
	build   func(core.GatewayProvider) (attempt, error)
}

// forward tries each usable provider in order. Nothing is written to w until
// one upstream answers with something other than a failover-worthy status, so
// the agent never sees a failed attempt.
func (s *Server) forward(w http.ResponseWriter, r *http.Request, cfg core.Gateway, rt route) {
	serving := servingProviders(cfg, rt.serves)
	if len(serving) == 0 {
		rt.dialect.writeErr(w, http.StatusServiceUnavailable,
			"网关里没有能处理这种请求的上游。gcccode gateway provider add <名称> --url URL --key KEY [--protocol openai|anthropic|gemini]")
		return
	}
	awake := s.awake(serving)
	if len(awake) == 0 {
		retry := s.retryAfter(serving)
		setRetryAfter(w, retry)
		rt.dialect.writeErr(w, http.StatusServiceUnavailable,
			"所有上游都在冷却中，最早 "+retry.Round(time.Second).String()+" 后恢复")
		return
	}
	var attempts []string
	for _, p := range awake {
		a, err := rt.build(p)
		if err != nil {
			attempts = append(attempts, p.Name+"："+err.Error())
			continue
		}
		resp, err := s.do(r.Context(), p, a)
		if err != nil {
			if r.Context().Err() != nil {
				return // the agent went away; nothing to answer
			}
			s.fail(p.Name, coolNetwork, "连接失败")
			attempts = append(attempts, p.Name+"：连接失败")
			continue
		}
		bad, cool, reason := s.judge(resp)
		if bad {
			resp.Body.Close()
			s.fail(p.Name, cool, reason)
			attempts = append(attempts, p.Name+"："+reason)
			continue
		}
		s.served(p.Name)
		if a.commit != nil {
			a.commit(w, resp)
		} else {
			copyResponse(w, resp)
		}
		return
	}
	setRetryAfter(w, s.retryAfter(serving))
	rt.dialect.writeErr(w, http.StatusServiceUnavailable, "所有上游都不可用："+strings.Join(attempts, "；"))
}

func setRetryAfter(w http.ResponseWriter, d time.Duration) {
	if d > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(d.Seconds())+1))
	}
}

func (s *Server) do(ctx context.Context, p core.GatewayProvider, a attempt) (*http.Response, error) {
	var rd io.Reader
	if a.body != nil {
		rd = bytes.NewReader(a.body)
	}
	target := a.url
	if target == "" {
		target = strings.TrimRight(p.BaseURL, "/") + a.path
	}
	req, err := http.NewRequestWithContext(ctx, a.method, target, rd)
	if err != nil {
		return nil, err
	}
	for k, vs := range a.header {
		req.Header[k] = append([]string(nil), vs...)
	}
	client := s.client
	if a.client != nil {
		client = a.client
	}
	// A subscription attempt carries its own credentials and the official
	// client's own headers; the gateway adds nothing of its own.
	if a.auth != nil {
		a.auth(req)
		return client.Do(req)
	}
	// Each protocol's own key header. The agent's Authorization (the local
	// token) is never in a.header, so it cannot reach an upstream.
	switch Protocol(p) {
	case ProtoAnthropic:
		req.Header.Set("X-Api-Key", p.APIKey)
		if req.Header.Get("Anthropic-Version") == "" {
			req.Header.Set("Anthropic-Version", anthropicVersion)
		}
	case ProtoGemini:
		req.Header.Set("X-Goog-Api-Key", p.APIKey)
	default:
		req.Header.Set("Authorization", "Bearer "+p.APIKey)
	}
	req.Header.Set("User-Agent", "gcccode-gateway/"+core.Version)
	return client.Do(req)
}

// pick copies only the named headers: an allowlist, so nothing
// account-specific from the agent travels upstream.
func pick(in http.Header, names ...string) http.Header {
	out := http.Header{}
	for _, n := range names {
		if vs := in.Values(n); len(vs) > 0 {
			out[http.CanonicalHeaderKey(n)] = append([]string(nil), vs...)
		}
	}
	return out
}

// judge decides whether resp should make the gateway try the next provider,
// and for how long this one is parked. Request-shaped errors (400, 404, 413,
// 422...) are not the provider's fault and go back to the agent unchanged,
// except a 400 that is really Anthropic's "credit balance is too low".
func (s *Server) judge(resp *http.Response) (bad bool, cool time.Duration, reason string) {
	code := resp.StatusCode
	switch {
	case code < 400:
		return false, 0, ""
	case code == http.StatusUnauthorized || code == http.StatusForbidden:
		return true, coolAuth, fmt.Sprintf("认证失败 (HTTP %d)", code)
	case code == http.StatusPaymentRequired:
		return true, coolQuota, "余额不足 (HTTP 402)"
	case code == http.StatusTooManyRequests:
		if quotaSpent(peekBody(resp)) {
			return true, coolQuota, "额度用完 (HTTP 429)"
		}
		if d := parseRetryAfter(resp.Header.Get("Retry-After"), s.now()); d > 0 {
			return true, d, "被限流 (HTTP 429)"
		}
		return true, coolRate, "被限流 (HTTP 429)"
	case code == http.StatusBadRequest:
		if quotaSpent(peekBody(resp)) {
			return true, coolQuota, "余额不足 (HTTP 400)"
		}
		return false, 0, ""
	case code >= 500:
		return true, coolServer, fmt.Sprintf("上游错误 (HTTP %d)", code)
	default:
		return false, 0, ""
	}
}

// peekBody reads the start of resp's body and puts it back, so the response
// can still be passed through whole.
func peekBody(resp *http.Response) []byte {
	peek, _ := io.ReadAll(io.LimitReader(resp.Body, peekLen))
	resp.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(peek), resp.Body), resp.Body}
	return peek
}

func quotaSpent(body []byte) bool {
	text := strings.ToLower(string(body))
	for _, w := range []string{"insufficient_quota", "exceeded your current quota", "credit balance", "billing", "balance is too low", "insufficient balance"} {
		if strings.Contains(text, w) {
			return true
		}
	}
	return false
}

func parseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if n, err := strconv.Atoi(v); err == nil && n > 0 {
		return time.Duration(n) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil && t.After(now) {
		return t.Sub(now)
	}
	return 0
}

var hopByHop = map[string]bool{
	"Connection": true, "Proxy-Connection": true, "Keep-Alive": true, "Transfer-Encoding": true,
	"Te": true, "Trailer": true, "Upgrade": true, "Proxy-Authenticate": true, "Proxy-Authorization": true,
}

// copyResponse commits to one upstream: headers go out, then the body is
// streamed through and flushed on every read so SSE arrives as it is produced.
func copyResponse(w http.ResponseWriter, resp *http.Response) {
	defer resp.Body.Close()
	h := w.Header()
	for k, vs := range resp.Header {
		if hopByHop[http.CanonicalHeaderKey(k)] {
			continue
		}
		for _, v := range vs {
			h.Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 32<<10)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}

// servingProviders are the enabled, well-formed providers a route can use,
// parked or not.
func servingProviders(cfg core.Gateway, serves func(core.GatewayProvider) bool) []core.GatewayProvider {
	var out []core.GatewayProvider
	for _, p := range cfg.Providers {
		if !p.Disabled && ValidateProvider(p) == nil && serves(p) {
			out = append(out, p)
		}
	}
	return out
}

// awake drops the providers still parked.
func (s *Server) awake(ps []core.GatewayProvider) []core.GatewayProvider {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []core.GatewayProvider
	for _, p := range ps {
		if o, ok := s.down[p.Name]; ok {
			if now.Before(o.Until) {
				continue
			}
			delete(s.down, p.Name)
		}
		out = append(out, p)
	}
	return out
}

// retryAfter is how long until the soonest parked provider in ps returns.
func (s *Server) retryAfter(ps []core.GatewayProvider) time.Duration {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	var best time.Duration
	for _, p := range ps {
		if o, ok := s.down[p.Name]; ok && o.Until.After(now) {
			if d := o.Until.Sub(now); best == 0 || d < best {
				best = d
			}
		}
	}
	return best
}

func (s *Server) fail(name string, cool time.Duration, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.down[name] = outage{Until: s.now().Add(cool), Reason: reason}
	st := s.statOf(name)
	st.Failed++
	st.LastReason = reason
}

func (s *Server) served(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statOf(name).Served++
}

func (s *Server) statOf(name string) *stat {
	st := s.stats[name]
	if st == nil {
		st = &stat{}
		s.stats[name] = st
	}
	return st
}

// ProviderStatus is one upstream as `gateway status` shows it. It never
// carries the API key.
type ProviderStatus struct {
	Name      string `json:"name"`
	Protocol  string `json:"protocol"`
	BaseURL   string `json:"base_url"`
	Model     string `json:"model,omitempty"`
	Disabled  bool   `json:"disabled,omitempty"`
	DownUntil int64  `json:"down_until,omitempty"`
	Reason    string `json:"reason,omitempty"`
	Served    int    `json:"served"`
	Failed    int    `json:"failed"`
}

// SubAccountStatus is one pooled subscription login as `gateway status` shows
// it. It never carries a token.
type SubAccountStatus struct {
	Provider  string  `json:"provider"`
	Kind      string  `json:"kind"`
	Account   string  `json:"account"`
	Label     string  `json:"label"`
	UsedPct   float64 `json:"used_pct"`
	DownUntil int64   `json:"down_until,omitempty"`
	Reason    string  `json:"reason,omitempty"`
	Served    int     `json:"served"`
	Failed    int     `json:"failed"`
}

// StatusReport is what /_gcccode/status returns.
type StatusReport struct {
	Version       string             `json:"version"`
	Providers     []ProviderStatus   `json:"providers"`
	Subscriptions []SubAccountStatus `json:"subscriptions,omitempty"`
}

func (s *Server) status(w http.ResponseWriter, r *http.Request, cfg core.Gateway) {
	now := s.now()
	var st *core.Store
	if ld, err := s.loadStore(); err == nil {
		st = ld
	}
	s.mu.Lock()
	rep := StatusReport{Version: core.Version, Providers: []ProviderStatus{}}
	for _, p := range cfg.Providers {
		if isSub(p) {
			rep.Subscriptions = append(rep.Subscriptions, s.subStatus(st, p, now)...)
			continue
		}
		ps := ProviderStatus{Name: p.Name, Protocol: Protocol(p), BaseURL: p.BaseURL, Model: p.Model, Disabled: p.Disabled}
		if o, ok := s.down[p.Name]; ok && o.Until.After(now) {
			ps.DownUntil, ps.Reason = o.Until.Unix(), o.Reason
		}
		if st := s.stats[p.Name]; st != nil {
			ps.Served, ps.Failed = st.Served, st.Failed
			if ps.Reason == "" {
				ps.Reason = st.LastReason
			}
		}
		rep.Providers = append(rep.Providers, ps)
	}
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(rep)
}

// subStatus lists the pooled accounts a subscription provider covers, with
// each one's usage and cooldown. The caller holds s.mu.
func (s *Server) subStatus(st *core.Store, p core.GatewayProvider, now time.Time) []SubAccountStatus {
	if st == nil {
		return nil
	}
	allow := map[string]bool{}
	for _, id := range p.Accounts {
		allow[id] = true
	}
	var out []SubAccountStatus
	for _, a := range st.ByKind(p.Subscription) {
		if len(allow) > 0 && !allow[a.ID] {
			continue
		}
		sa := SubAccountStatus{Provider: p.Name, Kind: p.Subscription, Account: a.ID, Label: a.Label}
		if q, ok := st.Quota[a.ID]; ok {
			eq := core.EffectiveQuota(q, now)
			for _, win := range []*core.Window{eq.FiveHour, eq.Weekly} {
				if win != nil && win.UsedPct > sa.UsedPct {
					sa.UsedPct = win.UsedPct
				}
			}
		}
		if o, ok := s.down[subKey(a.ID)]; ok && o.Until.After(now) {
			sa.DownUntil, sa.Reason = o.Until.Unix(), o.Reason
		}
		if stt := s.stats[subKey(a.ID)]; stt != nil {
			sa.Served, sa.Failed = stt.Served, stt.Failed
			if sa.Reason == "" {
				sa.Reason = stt.LastReason
			}
		}
		if a.Quarantined {
			sa.Reason = "需要重新登录：" + a.QuarantineReason
		}
		out = append(out, sa)
	}
	return out
}

// ValidateProvider reports why p cannot be used, or nil.
func ValidateProvider(p core.GatewayProvider) error {
	if strings.TrimSpace(p.Name) == "" {
		return errors.New("上游需要名称")
	}
	if isSub(p) {
		if p.Subscription != SubCodex && p.Subscription != SubClaude {
			return fmt.Errorf("上游 %s 的订阅类型只能是 codex 或 claude", p.Name)
		}
		if strings.TrimSpace(p.BaseURL) != "" || strings.TrimSpace(p.APIKey) != "" {
			return fmt.Errorf("上游 %s 是订阅号池，不需要 --url 或 --key", p.Name)
		}
		return nil
	}
	proto := Protocol(p)
	if proto != ProtoOpenAI && proto != ProtoAnthropic && proto != ProtoGemini {
		return fmt.Errorf("上游 %s 的协议只能是 openai、anthropic 或 gemini", p.Name)
	}
	if p.Responses && proto != ProtoOpenAI {
		return fmt.Errorf("上游 %s：只有 openai 协议的上游才能标 --responses", p.Name)
	}
	u, err := url.Parse(strings.TrimSpace(p.BaseURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("上游 %s 的地址要写成 %s 这种形式", p.Name, ExampleURL(proto))
	}
	if path := strings.TrimRight(u.Path, "/"); proto != ProtoOpenAI && (strings.HasSuffix(path, "/v1") || strings.HasSuffix(path, "/v1beta")) {
		return fmt.Errorf("上游 %s 的地址多带了版本路径，要写成 %s", p.Name, ExampleURL(proto))
	}
	if strings.TrimSpace(p.APIKey) == "" {
		return fmt.Errorf("上游 %s 缺少 API Key", p.Name)
	}
	return nil
}

// ExampleURL shows the base URL convention for each protocol: the same one
// its own SDK uses for OPENAI_BASE_URL, ANTHROPIC_BASE_URL and
// GOOGLE_GEMINI_BASE_URL.
func ExampleURL(proto string) string {
	switch proto {
	case ProtoAnthropic:
		return "https://api.anthropic.com（不带 /v1）"
	case ProtoGemini:
		return "https://generativelanguage.googleapis.com（不带 /v1beta）"
	default:
		return "https://api.openai.com/v1（带 /v1）"
	}
}

// loopbackHost accepts localhost and loopback IPs in a Host header, with or
// without a port.
func loopbackHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// ListenLoopback opens addr, refusing anything that is not a loopback address.
func ListenLoopback(addr string) (net.Listener, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("地址格式应为 host:port：%w", err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return nil, fmt.Errorf("网关只监听本机地址，拒绝 %s", host)
	}
	return net.Listen("tcp", addr)
}

// Serve answers on ln until ctx is cancelled, then shuts down gracefully.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
		return nil
	}
}
