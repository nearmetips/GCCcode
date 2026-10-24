package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nearmetips/DailiCode/internal/claude"
	"github.com/nearmetips/DailiCode/internal/core"
)

// claudeBackendURL is where Claude Code's subscription requests go.
const claudeBackendURL = "https://api.anthropic.com"

func claudeUpstreamBase() string {
	if v := loopbackOverride("GCCCODE_GATEWAY_TEST_CLAUDE_URL"); v != "" {
		return v
	}
	return claudeBackendURL
}

// claudeOAuthBeta is the beta flag a subscription (OAuth) request must carry.
const claudeOAuthBeta = "oauth-2025-04-20"

// claudeHeaders copies Claude Code's own request headers, drops the ones that
// carry the gateway token or clash with OAuth, and adds the OAuth beta flag.
func claudeHeaders(r *http.Request) http.Header {
	h := pick(r.Header,
		"Content-Type", "Accept", "Anthropic-Version", "Anthropic-Beta",
		"User-Agent", "x-app", "X-Claude-Code-Session-Id",
		"anthropic-dangerous-direct-browser-access",
		"X-Stainless-Lang", "X-Stainless-Package-Version", "X-Stainless-OS",
		"X-Stainless-Arch", "X-Stainless-Runtime", "X-Stainless-Runtime-Version",
		"X-Stainless-Retry-Count", "X-Stainless-Timeout",
	)
	if h.Get("Anthropic-Version") == "" {
		h.Set("Anthropic-Version", anthropicVersion)
	}
	betas := h.Values("Anthropic-Beta")
	has := false
	for _, b := range betas {
		if strings.Contains(b, claudeOAuthBeta) {
			has = true
		}
	}
	if !has {
		h.Add("Anthropic-Beta", claudeOAuthBeta)
	}
	return h
}

// claudeSessionKey is the conversation a Claude request belongs to.
func claudeSessionKey(r *http.Request, fields map[string]json.RawMessage) string {
	if r != nil {
		if v := strings.TrimSpace(r.Header.Get("X-Claude-Code-Session-Id")); v != "" {
			return v
		}
	}
	// metadata.user_id is a JSON string holding the session id.
	if md, ok := fields["metadata"]; ok {
		var m struct {
			UserID string `json:"user_id"`
		}
		if json.Unmarshal(md, &m) == nil && m.UserID != "" {
			var inner struct {
				SessionID string `json:"session_id"`
			}
			if json.Unmarshal([]byte(m.UserID), &inner) == nil {
				return inner.SessionID
			}
		}
	}
	return ""
}

// rewriteClaudeMetadata puts the chosen account's own device and account id
// into metadata.user_id, so the request looks like it came from that login.
func rewriteClaudeMetadata(body []byte, deviceID, accountUUID string) []byte {
	if deviceID == "" && accountUUID == "" {
		return body
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(body, &m) != nil {
		return body
	}
	var meta map[string]json.RawMessage
	if json.Unmarshal(m["metadata"], &meta) != nil || meta == nil {
		meta = map[string]json.RawMessage{}
	}
	var userID string
	_ = json.Unmarshal(meta["user_id"], &userID)
	var inner map[string]any
	if userID == "" || json.Unmarshal([]byte(userID), &inner) != nil || inner == nil {
		inner = map[string]any{}
	}
	if deviceID != "" {
		inner["device_id"] = deviceID
	}
	if accountUUID != "" {
		inner["account_uuid"] = accountUUID
	}
	encInner, err := json.Marshal(inner)
	if err != nil {
		return body
	}
	if meta["user_id"], err = json.Marshal(string(encInner)); err != nil {
		return body
	}
	if m["metadata"], err = json.Marshal(meta); err != nil {
		return body
	}
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}

// claudeQuota reads Anthropic's unified rate-limit headers; utilization is a
// fraction, so it is scaled to a percentage.
func claudeQuota(h http.Header, now time.Time) (core.Quota, bool) {
	q := core.Quota{Source: "gateway", UpdatedAt: now}
	got := false
	read := func(utilKey, resetKey string) *core.Window {
		u := h.Get(utilKey)
		if u == "" {
			return nil
		}
		frac, err := strconv.ParseFloat(u, 64)
		if err != nil {
			return nil
		}
		got = true
		w := &core.Window{UsedPct: frac * 100}
		if r := h.Get(resetKey); r != "" {
			if secs, err := strconv.ParseInt(r, 10, 64); err == nil && secs > 0 {
				w.ResetAt = secs
			}
		}
		return w
	}
	q.FiveHour = read("anthropic-ratelimit-unified-5h-utilization", "anthropic-ratelimit-unified-5h-reset")
	q.Weekly = read("anthropic-ratelimit-unified-7d-utilization", "anthropic-ratelimit-unified-7d-reset")
	if strings.EqualFold(h.Get("anthropic-ratelimit-unified-status"), "rejected") {
		q.Limited = true
		got = true
	}
	return q, got
}

// forwardClaudeSub routes a Messages request from Claude Code across the
// pooled Claude logins. An ongoing session stays on its own account; a new one
// may try each account in turn. It returns false, writing nothing, when the
// pool is spent and an API-key upstream should answer.
func (s *Server) forwardClaudeSub(w http.ResponseWriter, r *http.Request, cfg core.Gateway, p core.GatewayProvider, body []byte, fields map[string]json.RawMessage) bool {
	st, err := s.loadStore()
	if err != nil {
		dialectAnthropic.writeErr(w, http.StatusInternalServerError, err.Error())
		return true
	}
	key := claudeSessionKey(r, fields)
	bound := s.sessionBind(st, core.KindClaude, key)
	boundFinal := bound != ""
	accounts, _ := s.orderedAccounts(st, p, bound, boundFinal)
	fallback := s.hasAPIFallback(cfg, apiMessages)
	if len(accounts) == 0 {
		if fallback && !boundFinal {
			return false
		}
		dialectAnthropic.writeErr(w, http.StatusServiceUnavailable, "订阅号池里没有可用的 Claude 账号")
		return true
	}
	awake := s.awakeAccounts(accounts)
	if len(awake) == 0 {
		if fallback && !boundFinal {
			return false
		}
		retry := s.retryAfterAccounts(accounts)
		setRetryAfter(w, retry)
		dialectAnthropic.writeErr(w, http.StatusTooManyRequests, "这个会话绑定的 Claude 账号额度用完了，"+retry.Round(time.Second).String()+" 后恢复")
		return true
	}
	q := claudeQueryBeta(r)
	var tried []string
	for _, acc := range awake {
		resp, ok, reason := s.tryClaudeAccount(r, st.Settings, p, acc, body, fields, q)
		if reason == ctxGone {
			return true
		}
		if !ok {
			tried = append(tried, label(acc)+"："+reason)
			continue
		}
		s.served(subKey(acc.ID))
		s.bindSession(core.KindClaude, key, acc.ID)
		copyResponse(w, resp)
		return true
	}
	if fallback && !boundFinal {
		return false
	}
	setRetryAfter(w, s.retryAfterAccounts(accounts))
	dialectAnthropic.writeErr(w, http.StatusTooManyRequests, "Claude 号池都不可用："+strings.Join(tried, "；"))
	return true
}

// claudeQueryBeta keeps the ?beta=true marker Claude Code appends, dropping
// the gateway key from the query.
func claudeQueryBeta(r *http.Request) string {
	if r == nil {
		return ""
	}
	return queryWithoutKey(r.URL)
}

func (s *Server) tryClaudeAccount(r *http.Request, settings core.Settings, p core.GatewayProvider, acc core.Account, body []byte, fields map[string]json.RawMessage, query string) (*http.Response, bool, string) {
	deviceID := claude.DeviceID(settings, acc.ConfigDir)
	accountUUID := ""
	if id, ok := claude.ReadIdentity(settings, acc.ConfigDir); ok {
		accountUUID = id.AccountUUID
	}
	reqBody := rewriteClaudeMetadata(withModel(body, fields, p.Model), deviceID, accountUUID)
	busy := s.claudeBusy(acc)
	for forced := false; ; forced = true {
		tok, err := s.auth.Claude(ctxOf(r), settings, acc, busy, forced)
		if err != nil {
			cool, reason := s.classifyTokenErr(acc.ID, err)
			s.fail(subKey(acc.ID), cool, reason)
			return nil, false, reason
		}
		a := attempt{
			method: http.MethodPost, url: claudeUpstreamBase() + "/v1/messages" + query,
			body: reqBody, header: claudeHeaders(r), client: s.clientFor(acc),
			auth: func(req *http.Request) {
				req.Header.Set("Authorization", "Bearer "+tok.Access)
				req.Header.Del("X-Api-Key")
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
		if q, ok := claudeQuota(resp.Header, s.now()); ok {
			s.recordQuota(acc.ID, q)
		}
		if resp.StatusCode == http.StatusUnauthorized && !forced {
			resp.Body.Close()
			continue
		}
		bad, cool, reason := s.judgeClaudeSub(resp, acc)
		if bad {
			resp.Body.Close()
			if cool > 0 {
				s.fail(subKey(acc.ID), cool, reason)
			}
			return nil, false, reason
		}
		return resp, true, ""
	}
}

// claudeBusy reports whether a pooled Claude Code session is already running
// on the account; if so, its own client renews the token and the gateway must
// not refresh in parallel.
func (s *Server) claudeBusy(acc core.Account) bool {
	st, err := s.loadStore()
	if err != nil {
		return false
	}
	return st.Busy(acc.ID)
}

func (s *Server) judgeClaudeSub(resp *http.Response, acc core.Account) (bad bool, cool time.Duration, reason string) {
	code := resp.StatusCode
	switch {
	case code < 400:
		return false, 0, ""
	case code == http.StatusUnauthorized, code == http.StatusForbidden:
		s.quarantine(acc.ID, "认证失败，需要重新登录")
		return true, 0, "认证失败"
	case code == http.StatusTooManyRequests:
		until := int64(0)
		if r := resp.Header.Get("anthropic-ratelimit-unified-5h-reset"); r != "" {
			until, _ = strconv.ParseInt(r, 10, 64)
		}
		if d := parseRetryAfter(resp.Header.Get("Retry-After"), s.now()); until == 0 && d > 0 {
			until = s.now().Add(d).Unix()
		}
		s.markExhausted(acc.ID, until, "额度用完")
		return true, 0, "额度用完"
	case code >= 500, code == 529:
		return true, coolServer, "上游错误"
	default:
		return false, 0, ""
	}
}

// countTokensClaudeSub answers a token count on the account the session is
// bound to, or the first usable one. It returns false to fall back to an
// estimate when no account can answer.
func (s *Server) countTokensClaudeSub(w http.ResponseWriter, r *http.Request, p core.GatewayProvider, body []byte, fields map[string]json.RawMessage) bool {
	st, err := s.loadStore()
	if err != nil {
		return false
	}
	key := claudeSessionKey(r, fields)
	bound := s.sessionBind(st, core.KindClaude, key)
	accounts, _ := s.orderedAccounts(st, p, bound, bound != "")
	awake := s.awakeAccounts(accounts)
	if len(awake) == 0 {
		return false
	}
	acc := awake[0]
	tok, err := s.auth.Claude(ctxOf(r), st.Settings, acc, s.claudeBusy(acc), false)
	if err != nil {
		return false
	}
	a := attempt{
		method: http.MethodPost, url: claudeUpstreamBase() + "/v1/messages/count_tokens" + claudeQueryBeta(r),
		body: withModel(body, fields, p.Model), header: claudeHeaders(r), client: s.clientFor(acc),
		auth: func(req *http.Request) {
			req.Header.Set("Authorization", "Bearer "+tok.Access)
			req.Header.Del("X-Api-Key")
		},
	}
	resp, err := s.do(ctxOf(r), p, a)
	if err != nil {
		return false
	}
	if resp.StatusCode >= 400 {
		resp.Body.Close()
		return false
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.Copy(w, io.LimitReader(resp.Body, maxBody))
	return true
}
