package gateway

import (
	"errors"
	"fmt"
	"slices"

	"github.com/nearmetips/DailiCode/internal/core"
)

// Routable are the official clients use-gateway can send through the
// gateway. Grok has no protocol here; Antigravity only signs in with Google.
var Routable = []string{core.KindClaude, core.KindCodex, core.KindGemini}

// ClientProtocol is the protocol an official client speaks to the gateway.
func ClientProtocol(kind string) string {
	switch kind {
	case core.KindClaude:
		return ProtoAnthropic
	case core.KindGemini:
		return ProtoGemini
	default:
		return ProtoOpenAI
	}
}

// Uses reports whether use-gateway is on for kind.
func Uses(s core.Settings, kind string) bool { return slices.Contains(s.Gateway.Use, kind) }

// CanRoute is nil when cfg has an upstream that can answer kind's official
// client, and otherwise says what is missing.
func CanRoute(cfg core.Gateway, kind string) error {
	var serves func(core.GatewayProvider) bool
	var missing string
	switch kind {
	case core.KindClaude:
		serves, missing = servesMessages, "网关里没有能接 Claude Code 的上游：要一个 anthropic 上游，或一个 openai 上游（会翻译）"
	case core.KindCodex:
		serves, missing = servesResponses, "Codex 用 Responses 接口，网关里没有标了 --responses 的 openai 上游"
	case core.KindGemini:
		serves, missing = isGemini, "网关里没有 gemini 上游"
	case core.KindGrok:
		return errors.New("Grok 不能走网关：网关没有 xAI 的协议")
	case core.KindAgy:
		return errors.New("Antigravity 不能走网关：它只用 Google 账号登录，走号池")
	default:
		return fmt.Errorf("未知的客户端：%s", kind)
	}
	if len(servingProviders(cfg, serves)) == 0 {
		return errors.New(missing)
	}
	return nil
}
