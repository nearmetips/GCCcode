package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/nearmetips/DailiCode/internal/core"
)

// dialect is how the gateway words its own errors: in the shape of the
// protocol the agent spoke, so the agent shows them properly.
type dialect int

const (
	dialectOpenAI dialect = iota
	dialectAnthropic
	dialectGemini
)

func (d dialect) writeErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	var body any
	switch d {
	case dialectAnthropic:
		body = map[string]any{"type": "error", "error": map[string]any{"type": anthropicErrType(status), "message": msg}}
	case dialectGemini:
		body = map[string]any{"error": map[string]any{"code": status, "message": msg, "status": geminiStatus(status)}}
	default:
		body = map[string]any{"error": map[string]any{"message": msg, "type": openAIErrType(status), "code": nil}}
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(body)
}

func openAIErrType(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return "invalid_api_key"
	case http.StatusTooManyRequests:
		return "rate_limit_exceeded"
	case http.StatusServiceUnavailable:
		return "gateway_unavailable"
	}
	if status < 500 {
		return "invalid_request_error"
	}
	return "server_error"
}

func anthropicErrType(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "invalid_request_error"
	case http.StatusUnauthorized:
		return "authentication_error"
	case http.StatusForbidden:
		return "permission_error"
	case http.StatusNotFound:
		return "not_found_error"
	case http.StatusRequestEntityTooLarge:
		return "request_too_large"
	case http.StatusTooManyRequests:
		return "rate_limit_error"
	case http.StatusServiceUnavailable, 529:
		return "overloaded_error"
	}
	if status < 500 {
		return "invalid_request_error"
	}
	return "api_error"
}

func geminiStatus(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "INVALID_ARGUMENT"
	case http.StatusUnauthorized:
		return "UNAUTHENTICATED"
	case http.StatusForbidden:
		return "PERMISSION_DENIED"
	case http.StatusNotFound:
		return "NOT_FOUND"
	case http.StatusTooManyRequests:
		return "RESOURCE_EXHAUSTED"
	case http.StatusServiceUnavailable:
		return "UNAVAILABLE"
	}
	return "INTERNAL"
}

func isOpenAI(p core.GatewayProvider) bool    { return Protocol(p) == ProtoOpenAI }
func isAnthropic(p core.GatewayProvider) bool { return Protocol(p) == ProtoAnthropic }
func isGemini(p core.GatewayProvider) bool    { return Protocol(p) == ProtoGemini }

// Subscription provider kinds.
const (
	SubCodex  = core.KindCodex
	SubClaude = core.KindClaude
)

func isSub(p core.GatewayProvider) bool     { return p.Subscription != "" }
func codexSub(p core.GatewayProvider) bool  { return p.Subscription == SubCodex }
func claudeSub(p core.GatewayProvider) bool { return p.Subscription == SubClaude }

// The api* predicates match only API-key upstreams, which the plain forward
// path can reach; subscription providers are routed separately.
func apiOpenAI(p core.GatewayProvider) bool    { return !isSub(p) && isOpenAI(p) }
func apiGemini(p core.GatewayProvider) bool    { return !isSub(p) && isGemini(p) }
func apiAnthropic(p core.GatewayProvider) bool { return !isSub(p) && isAnthropic(p) }
func apiResponses(p core.GatewayProvider) bool { return apiOpenAI(p) && p.Responses }

// apiMessages: an Anthropic upstream takes Messages as is, an OpenAI one translated.
func apiMessages(p core.GatewayProvider) bool { return apiAnthropic(p) || apiOpenAI(p) }

// servesMessages / servesResponses say whether any upstream, API-key or
// subscription, can answer this inbound shape. CanRoute and Inbound use them.
func servesMessages(p core.GatewayProvider) bool {
	return apiMessages(p) || claudeSub(p) || codexSub(p)
}
func servesResponses(p core.GatewayProvider) bool { return apiResponses(p) || codexSub(p) }

// readJSONBody reads a JSON object body, answering the agent itself when it
// is not one.
func readJSONBody(w http.ResponseWriter, r *http.Request, d dialect) ([]byte, map[string]json.RawMessage, bool) {
	if r.Method != http.MethodPost {
		d.writeErr(w, http.StatusMethodNotAllowed, "只支持 POST")
		return nil, nil, false
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		d.writeErr(w, http.StatusRequestEntityTooLarge, "请求体太大或读取失败")
		return nil, nil, false
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		d.writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return nil, nil, false
	}
	return body, fields, true
}

// withModel is body with its "model" replaced by model, or body itself when
// model is empty.
func withModel(body []byte, fields map[string]json.RawMessage, model string) []byte {
	if model == "" {
		return body
	}
	next := make(map[string]json.RawMessage, len(fields))
	for k, v := range fields {
		next[k] = v
	}
	next["model"], _ = json.Marshal(model)
	b, err := json.Marshal(next)
	if err != nil {
		return body
	}
	return b
}

// queryWithoutKey is u's query minus Gemini's ?key=, which carries the local
// token and must not travel upstream.
func queryWithoutKey(u *url.URL) string {
	q := u.Query()
	q.Del("key")
	if len(q) == 0 {
		return ""
	}
	return "?" + q.Encode()
}

func (s *Server) chat(w http.ResponseWriter, r *http.Request, cfg core.Gateway) {
	body, fields, ok := readJSONBody(w, r, dialectOpenAI)
	if !ok {
		return
	}
	if p, ok := subProvider(cfg, SubCodex); ok {
		if s.forwardChatViaCodex(w, r, cfg, p, body, fields) {
			return
		}
	}
	hdr := pick(r.Header, "Content-Type", "Accept")
	s.forward(w, r, cfg, route{
		dialect: dialectOpenAI,
		serves:  apiOpenAI,
		build: func(p core.GatewayProvider) (attempt, error) {
			return attempt{method: http.MethodPost, path: "/chat/completions", body: withModel(body, fields, p.Model), header: hdr}, nil
		},
	})
}

// responses passes OpenAI Responses (what Codex speaks) to upstreams. A
// pooled Codex subscription answers it natively; otherwise an API-key OpenAI
// upstream marked --responses does.
func (s *Server) responses(w http.ResponseWriter, r *http.Request, cfg core.Gateway) {
	body, fields, ok := readJSONBody(w, r, dialectOpenAI)
	if !ok {
		return
	}
	if p, ok := subProvider(cfg, SubCodex); ok {
		if s.forwardCodexSub(w, r, cfg, p, body, fields) {
			return
		}
	}
	hdr := pick(r.Header, "Content-Type", "Accept", "OpenAI-Beta")
	s.forward(w, r, cfg, route{
		dialect: dialectOpenAI,
		serves:  apiResponses,
		build: func(p core.GatewayProvider) (attempt, error) {
			return attempt{method: http.MethodPost, path: "/responses", body: withModel(body, fields, p.Model), header: hdr}, nil
		},
	})
}

func (s *Server) models(w http.ResponseWriter, r *http.Request, cfg core.Gateway) {
	if r.Method != http.MethodGet {
		dialectOpenAI.writeErr(w, http.StatusMethodNotAllowed, "只支持 GET")
		return
	}
	hdr := pick(r.Header, "Accept")
	q := queryWithoutKey(r.URL)
	s.forward(w, r, cfg, route{
		dialect: dialectOpenAI,
		serves:  apiOpenAI,
		build: func(core.GatewayProvider) (attempt, error) {
			return attempt{method: http.MethodGet, path: "/models" + q, header: hdr}, nil
		},
	})
}

// messages serves Anthropic Messages. Claude Code on a pooled Claude
// subscription is answered natively; any other agent, and the API-key path,
// go to an Anthropic upstream as is or an OpenAI one (including a pooled Codex
// subscription) through translation.
func (s *Server) messages(w http.ResponseWriter, r *http.Request, cfg core.Gateway) {
	body, fields, ok := readJSONBody(w, r, dialectAnthropic)
	if !ok {
		return
	}
	if p, ok := subProvider(cfg, SubClaude); ok && isClaudeCode(r) {
		if s.forwardClaudeSub(w, r, cfg, p, body, fields) {
			return
		}
	}
	if p, ok := subProvider(cfg, SubCodex); ok && !isClaudeCode(r) {
		if s.forwardMessagesViaCodex(w, r, cfg, p, body) {
			return
		}
	}
	q := queryWithoutKey(r.URL)
	hdr := pick(r.Header, "Content-Type", "Accept", "Anthropic-Version", "Anthropic-Beta")
	s.forward(w, r, cfg, route{
		dialect: dialectAnthropic,
		serves:  apiMessages,
		build: func(p core.GatewayProvider) (attempt, error) {
			if isAnthropic(p) {
				return attempt{method: http.MethodPost, path: "/v1/messages" + q, body: withModel(body, fields, p.Model), header: hdr}, nil
			}
			chatBody, stream, model, err := anthropicToChat(body, p.Model)
			if err != nil {
				return attempt{}, err
			}
			return attempt{
				method: http.MethodPost, path: "/chat/completions", body: chatBody,
				header: http.Header{"Content-Type": {"application/json"}},
				commit: func(w http.ResponseWriter, resp *http.Response) { chatToAnthropic(w, resp, stream, model) },
			}, nil
		},
	})
}

// isClaudeCode reports whether a request came from Claude Code itself, by its
// user agent. Only Claude Code may ride a pooled Claude subscription; other
// agents speaking Anthropic are routed elsewhere.
func isClaudeCode(r *http.Request) bool {
	if r == nil {
		return false
	}
	ua := strings.ToLower(r.Header.Get("User-Agent"))
	return strings.Contains(ua, "claude-cli") || strings.EqualFold(r.Header.Get("x-app"), "cli")
}

// countTokens passes through to an Anthropic upstream. With none available
// (only OpenAI upstreams, or the Anthropic ones cooling down) it answers with
// a rough estimate rather than an error the agent would surface.
func (s *Server) countTokens(w http.ResponseWriter, r *http.Request, cfg core.Gateway) {
	body, fields, ok := readJSONBody(w, r, dialectAnthropic)
	if !ok {
		return
	}
	if p, ok := subProvider(cfg, SubClaude); ok && isClaudeCode(r) {
		if s.countTokensClaudeSub(w, r, p, body, fields) {
			return
		}
	}
	if len(s.awake(servingProviders(cfg, apiAnthropic))) == 0 {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]int{"input_tokens": len(body)/4 + 1})
		return
	}
	q := queryWithoutKey(r.URL)
	hdr := pick(r.Header, "Content-Type", "Accept", "Anthropic-Version", "Anthropic-Beta")
	s.forward(w, r, cfg, route{
		dialect: dialectAnthropic,
		serves:  apiAnthropic,
		build: func(p core.GatewayProvider) (attempt, error) {
			return attempt{method: http.MethodPost, path: "/v1/messages/count_tokens" + q, body: withModel(body, fields, p.Model), header: hdr}, nil
		},
	})
}

// gemini passes Google Gemini API calls (Gemini CLI with
// GOOGLE_GEMINI_BASE_URL) to Gemini upstreams.
func (s *Server) gemini(w http.ResponseWriter, r *http.Request, cfg core.Gateway) {
	var body []byte
	if r.Method == http.MethodPost {
		b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
		if err != nil {
			dialectGemini.writeErr(w, http.StatusRequestEntityTooLarge, "请求体太大或读取失败")
			return
		}
		body = b
	}
	q := queryWithoutKey(r.URL)
	hdr := pick(r.Header, "Content-Type", "Accept")
	s.forward(w, r, cfg, route{
		dialect: dialectGemini,
		serves:  apiGemini,
		build: func(p core.GatewayProvider) (attempt, error) {
			return attempt{method: r.Method, path: geminiModelPath(r.URL.Path, p.Model) + q, body: body, header: hdr}, nil
		},
	})
}

// geminiModelPath swaps the model in /v1beta/models/<model>:<method>.
func geminiModelPath(path, model string) string {
	if model == "" {
		return path
	}
	i := strings.Index(path, "/models/")
	if i < 0 {
		return path
	}
	rest := path[i+len("/models/"):]
	j := strings.IndexByte(rest, ':')
	if j < 0 {
		return path[:i] + "/models/" + model
	}
	return path[:i] + "/models/" + model + rest[j:]
}
