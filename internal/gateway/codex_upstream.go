package gateway

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/nearmetips/DailiCode/internal/core"
)

// codexBackendURL is where Codex's ChatGPT-subscription requests go. A test
// may point it at a loopback stub through the environment.
const codexBackendURL = "https://chatgpt.com/backend-api/codex/responses"

func codexUpstreamURL() string {
	if v := loopbackOverride("GCCCODE_GATEWAY_TEST_CODEX_URL"); v != "" {
		return v
	}
	return codexBackendURL
}

// loopbackOverride reads a URL from the environment but only honours it when
// it points at the local machine, so a stray variable cannot divert a login
// to a remote host.
func loopbackOverride(name string) string {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return ""
	}
	u, err := url.Parse(v)
	if err != nil || u.Host == "" || !loopbackHost(u.Host) {
		return ""
	}
	return v
}

// codexHeaders copies the Codex client headers the ChatGPT backend expects,
// leaving out anything that carries the local gateway token.
func codexHeaders(r *http.Request) http.Header {
	h := pick(r.Header,
		"Content-Type", "Accept", "OpenAI-Beta",
		"originator", "session_id", "session-id", "thread-id",
		"x-client-request-id", "x-codex-beta-features", "x-codex-turn-metadata",
		"x-codex-window-id", "x-openai-internal-codex-responses-lite",
		"User-Agent",
	)
	if h.Get("originator") == "" {
		h.Set("originator", "codex_cli_rs")
	}
	return h
}

// codexSessionKey is the conversation a Codex request belongs to.
func codexSessionKey(r *http.Request, fields map[string]json.RawMessage) string {
	if r != nil {
		for _, name := range []string{"session_id", "session-id", "thread-id"} {
			if v := strings.TrimSpace(r.Header.Get(name)); v != "" {
				return v
			}
		}
	}
	return fieldString(fields, "prompt_cache_key")
}

// codexDropReasoning removes encrypted reasoning items from the input: they
// were produced under one account and will not verify under another.
func codexDropReasoning(body []byte) []byte {
	var m map[string]json.RawMessage
	if json.Unmarshal(body, &m) != nil {
		return body
	}
	var input []json.RawMessage
	if json.Unmarshal(m["input"], &input) != nil {
		return body
	}
	kept := input[:0]
	for _, item := range input {
		var t struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(item, &t) == nil && t.Type == "reasoning" {
			continue
		}
		kept = append(kept, item)
	}
	if len(kept) == len(input) {
		return body
	}
	m["input"], _ = json.Marshal(kept)
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}

// codexQuota reads the rate-limit headers the ChatGPT backend returns.
func codexQuota(h http.Header, now time.Time) (core.Quota, bool) {
	q := core.Quota{Source: "gateway", UpdatedAt: now}
	got := false
	read := func(usedKey, resetKey string) *core.Window {
		used := h.Get(usedKey)
		if used == "" {
			return nil
		}
		pct, err := strconv.ParseFloat(used, 64)
		if err != nil {
			return nil
		}
		got = true
		w := &core.Window{UsedPct: pct}
		if r := h.Get(resetKey); r != "" {
			if secs, err := strconv.ParseInt(r, 10, 64); err == nil && secs > 0 {
				w.ResetAt = secs
			}
		}
		return w
	}
	q.FiveHour = read("x-codex-primary-used-percent", "x-codex-primary-reset-at")
	q.Weekly = read("x-codex-secondary-used-percent", "x-codex-secondary-reset-at")
	if t := h.Get("x-codex-rate-limit-reached-type"); t != "" {
		q.Limited = true
		got = true
	}
	return q, got
}

// codexBackendReset reads the reset time out of a usage-limit error body.
func codexBackendReset(body []byte) (int64, bool) {
	var e struct {
		ResetsAt int64 `json:"resets_at"`
		Error    struct {
			ResetsAt int64 `json:"resets_at"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &e) != nil {
		return 0, false
	}
	if e.ResetsAt > 0 {
		return e.ResetsAt, true
	}
	if e.Error.ResetsAt > 0 {
		return e.Error.ResetsAt, true
	}
	return 0, false
}

// forwardCodexSub routes a Responses request across the pooled Codex logins.
// It returns false, without writing anything, when the pool is spent and an
// API-key upstream should answer instead.
func (s *Server) forwardCodexSub(w http.ResponseWriter, r *http.Request, cfg core.Gateway, p core.GatewayProvider, body []byte, fields map[string]json.RawMessage) bool {
	return s.routeCodexSub(w, r, cfg, p, codexSessionKey(r, fields), dialectOpenAI,
		func(_ core.Account, changed bool) []byte {
			if changed {
				return codexDropReasoning(body)
			}
			return body
		},
		copyResponse, s.hasAPIFallback(cfg, apiResponses), "Codex")
}

// routeCodexSub is the shared loop that sends a Responses request across the
// pooled Codex accounts. buildBody makes the request for one account (changed
// is true when the session last ran on a different login), commit writes a
// chosen answer, and fallback says whether an API-key upstream can take over
// once the pool is spent.
func (s *Server) routeCodexSub(w http.ResponseWriter, r *http.Request, cfg core.Gateway, p core.GatewayProvider, key string, d dialect, buildBody func(acc core.Account, changed bool) []byte, commit func(http.ResponseWriter, *http.Response), fallback bool, what string) bool {
	st, err := s.loadStore()
	if err != nil {
		d.writeErr(w, http.StatusInternalServerError, err.Error())
		return true
	}
	bound := s.sessionBind(st, core.KindCodex, key)
	accounts, _ := s.orderedAccounts(st, p, bound, false)
	if len(accounts) == 0 {
		if fallback {
			return false
		}
		d.writeErr(w, http.StatusServiceUnavailable, "订阅号池里没有可用的 "+what+" 账号")
		return true
	}
	awake := s.awakeAccounts(accounts)
	if len(awake) == 0 {
		if fallback {
			return false
		}
		setRetryAfter(w, s.retryAfterAccounts(accounts))
		d.writeErr(w, http.StatusServiceUnavailable, what+" 号池都在冷却中")
		return true
	}
	var tried []string
	for _, acc := range awake {
		changed := bound != "" && bound != acc.ID
		resp, ok, reason := s.tryCodexAccount(r, st.Settings, p, acc, buildBody(acc, changed))
		if reason == ctxGone {
			return true
		}
		if !ok {
			tried = append(tried, label(acc)+"："+reason)
			continue
		}
		s.served(subKey(acc.ID))
		s.bindSession(core.KindCodex, key, acc.ID)
		commit(w, resp)
		return true
	}
	if fallback {
		return false
	}
	setRetryAfter(w, s.retryAfterAccounts(accounts))
	d.writeErr(w, http.StatusServiceUnavailable, what+" 号池都不可用："+strings.Join(tried, "；"))
	return true
}

const ctxGone = "__ctx_gone__"

// tryCodexAccount sends one request to one account, refreshing the token once
// on a 401. On success it returns the live response for the caller to stream.
func (s *Server) tryCodexAccount(r *http.Request, settings core.Settings, p core.GatewayProvider, acc core.Account, reqBody []byte) (*http.Response, bool, string) {
	for forced := false; ; forced = true {
		tok, err := s.auth.Codex(ctxOf(r), settings, acc, forced)
		if err != nil {
			cool, reason := s.classifyTokenErr(acc.ID, err)
			s.fail(subKey(acc.ID), cool, reason)
			return nil, false, reason
		}
		a := attempt{
			method: http.MethodPost, url: codexUpstreamURL(), body: reqBody,
			header: codexHeaders(r), client: s.clientFor(acc),
			auth: func(req *http.Request) {
				req.Header.Set("Authorization", "Bearer "+tok.Access)
				if tok.AccountID != "" {
					req.Header.Set("chatgpt-account-id", tok.AccountID)
				}
			},
		}
		resp, err := s.do(ctxOf(r), p, a)
		if err != nil {
			if r != nil && r.Context().Err() != nil {
				return nil, false, ctxGone
			}
			s.fail(subKey(acc.ID), coolNetwork, "连接失败")
			return nil, false, "连接失败"
		}
		if q, ok := codexQuota(resp.Header, s.now()); ok {
			s.recordQuota(acc.ID, q)
		}
		if resp.StatusCode == http.StatusUnauthorized && !forced {
			resp.Body.Close()
			continue
		}
		bad, cool, reason := s.judgeCodexSub(resp, acc)
		if bad {
			resp.Body.Close()
			if cool == 0 {
				return nil, false, reason
			}
			s.fail(subKey(acc.ID), cool, reason)
			return nil, false, reason
		}
		return resp, true, ""
	}
}

// judgeCodexSub decides whether to fail over, recording a spent account's
// reset so the scheduler brings it back on time.
func (s *Server) judgeCodexSub(resp *http.Response, acc core.Account) (bad bool, cool time.Duration, reason string) {
	code := resp.StatusCode
	switch {
	case code < 400:
		return false, 0, ""
	case code == http.StatusUnauthorized, code == http.StatusForbidden:
		s.quarantine(acc.ID, "认证失败，需要重新登录")
		return true, 0, "认证失败"
	case code == http.StatusTooManyRequests, code == http.StatusPaymentRequired:
		body := peekBody(resp)
		until, _ := codexBackendReset(body)
		s.markExhausted(acc.ID, until, "额度用完")
		return true, 0, "额度用完"
	case code == http.StatusBadRequest:
		if quotaSpent(peekBody(resp)) {
			s.markExhausted(acc.ID, 0, "额度用完")
			return true, 0, "额度用完"
		}
		return false, 0, ""
	case code >= 500:
		return true, coolServer, "上游错误"
	default:
		return false, 0, ""
	}
}
