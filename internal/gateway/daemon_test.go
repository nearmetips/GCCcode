package gateway

import (
	"reflect"
	"testing"

	"github.com/nearmetips/DailiCode/internal/core"
)

func TestPointsAt(t *testing.T) {
	t.Setenv("GCCCODE_HOME", t.TempDir())
	var s core.Settings
	for raw, want := range map[string]bool{
		"http://127.0.0.1:3426/v1":     true,
		"http://localhost:3426":        true,
		"http://[::1]:3426/v1beta":     true,
		"http://127.0.0.1:3427/v1":     false,
		"https://api.openai.com/v1":    false,
		"http://10.0.0.5:3426/v1":      false,
		"":                             false,
		"not a url":                    false,
		"https://relay.example.com/v1": false,
	} {
		if got := PointsAt(s, raw); got != want {
			t.Errorf("default addr, %q: got %v", raw, got)
		}
	}
	s.Gateway.Addr = "127.0.0.1:5051"
	if PointsAt(s, "http://127.0.0.1:3426/v1") || !PointsAt(s, "http://127.0.0.1:5051/v1") {
		t.Fatal("saved addr not used")
	}
}

func TestEnvPerProtocol(t *testing.T) {
	got := map[string]string{}
	for _, proto := range []string{ProtoOpenAI, ProtoAnthropic, ProtoGemini} {
		for _, v := range Env(proto, "127.0.0.1:3426", "gw-x") {
			got[v.Name] = v.Value
		}
	}
	want := map[string]string{
		"OPENAI_BASE_URL":        "http://127.0.0.1:3426/v1",
		"OPENAI_API_KEY":         "gw-x",
		"ANTHROPIC_BASE_URL":     "http://127.0.0.1:3426",
		"ANTHROPIC_AUTH_TOKEN":   "gw-x",
		"GOOGLE_GEMINI_BASE_URL": "http://127.0.0.1:3426",
		"GEMINI_API_KEY":         "gw-x",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}

func TestCanRoute(t *testing.T) {
	openai := core.GatewayProvider{Name: "o", BaseURL: "https://x.example.com/v1", APIKey: "k"}
	responses := openai
	responses.Responses = true
	anth := core.GatewayProvider{Name: "a", BaseURL: "https://api.anthropic.com", APIKey: "k", Protocol: ProtoAnthropic}
	gem := core.GatewayProvider{Name: "g", BaseURL: "https://generativelanguage.googleapis.com", APIKey: "k", Protocol: ProtoGemini}
	off := gem
	off.Disabled = true
	for _, c := range []struct {
		kind string
		ps   []core.GatewayProvider
		ok   bool
	}{
		{core.KindClaude, []core.GatewayProvider{anth}, true},
		{core.KindClaude, []core.GatewayProvider{openai}, true},
		{core.KindClaude, []core.GatewayProvider{gem}, false},
		{core.KindCodex, []core.GatewayProvider{openai, anth}, false},
		{core.KindCodex, []core.GatewayProvider{responses}, true},
		{core.KindGemini, []core.GatewayProvider{gem}, true},
		{core.KindGemini, []core.GatewayProvider{off, openai}, false},
		{core.KindGrok, []core.GatewayProvider{openai, anth, gem}, false},
		{core.KindAgy, []core.GatewayProvider{openai, anth, gem}, false},
	} {
		if err := CanRoute(core.Gateway{Providers: c.ps}, c.kind); (err == nil) != c.ok {
			t.Errorf("%s with %v: %v", c.kind, c.ps, err)
		}
	}
}

func TestInbound(t *testing.T) {
	p := func(proto string, disabled bool) core.GatewayProvider {
		return core.GatewayProvider{Name: "p-" + proto, BaseURL: "https://x.example.com", APIKey: "k", Protocol: proto, Disabled: disabled}
	}
	for _, c := range []struct {
		ps   []core.GatewayProvider
		want []string
	}{
		{nil, nil},
		{[]core.GatewayProvider{p("", false)}, []string{ProtoOpenAI, ProtoAnthropic}},
		{[]core.GatewayProvider{p(ProtoAnthropic, false)}, []string{ProtoAnthropic}},
		{[]core.GatewayProvider{p(ProtoGemini, false), p("", true)}, []string{ProtoGemini}},
		{[]core.GatewayProvider{p(ProtoGemini, false), p(ProtoAnthropic, false), p("", false)}, []string{ProtoOpenAI, ProtoAnthropic, ProtoGemini}},
	} {
		if got := Inbound(core.Gateway{Providers: c.ps}); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%+v: got %v, want %v", c.ps, got, c.want)
		}
	}
}
