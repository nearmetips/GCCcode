package launcher

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/nearmetips/DailiCode/internal/bins"
	"github.com/nearmetips/DailiCode/internal/claude"
	"github.com/nearmetips/DailiCode/internal/core"
	"github.com/nearmetips/DailiCode/internal/gateway"
	"github.com/nearmetips/DailiCode/internal/gemini"
	"github.com/nearmetips/DailiCode/internal/handoff"
	"github.com/nearmetips/DailiCode/internal/scheduler"
)

// EnsureGateway returns the running gateway, starting it if needed. The
// command layer sets it, since starting means running this binary again;
// left nil, a stopped gateway is an error.
var EnsureGateway func() (gateway.Process, error)

// codexGatewayKeyEnv carries the local token to Codex. Codex reads it through
// a provider defined on the command line, so only this local token travels to
// the gateway; the gateway supplies whichever pooled or API-key login answers.
const codexGatewayKeyEnv = "GCCCODE_GATEWAY_KEY"

// runGateway starts the official client on the gateway instead of a pooled
// account: no account is picked, and there is no switching or handoff
// afterwards, because the gateway fails over between its own upstreams.
func runGateway(st *core.Store, o Options, note func(string, ...any)) error {
	if err := gateway.CanRoute(st.Settings.Gateway, o.Kind); err != nil {
		return fmt.Errorf("%w。gcccode use-gateway off %s 恢复直连官方", err, o.Kind)
	}
	p, running := gateway.Running()
	if !running {
		if EnsureGateway == nil {
			return errors.New("网关没有在运行：gcccode gateway start")
		}
		var err error
		if p, err = EnsureGateway(); err != nil {
			return fmt.Errorf("启动网关失败：%w", err)
		}
		note("已启动网关 http://%s。", p.Addr)
		if st, err = core.Load(); err != nil {
			return err
		}
	}
	token := st.Settings.Gateway.Token
	if token == "" {
		return errors.New("网关还没有本机 token：gcccode gateway start")
	}
	var (
		bin  string
		args []string
		env  []string
		err  error
	)
	switch o.Kind {
	case core.KindClaude:
		dir := core.GatewayProfileDir(core.KindClaude)
		if err := claude.EnsureProfile(st.Settings, dir); err != nil {
			return err
		}
		if err := claude.MarkOnboarded(st.Settings, dir); err != nil {
			return err
		}
		if bin, err = bins.Claude(st.Settings); err != nil {
			return fmt.Errorf("找不到 claude 命令：%w", err)
		}
		if args, err = gatewayClaudeArgs(st, o, note); err != nil {
			return err
		}
		env = claudeEnv(os.Environ(), st.Settings, core.Account{ConfigDir: dir})
		env = withVars(env, gateway.Env(gateway.ProtoAnthropic, p.Addr, token))
	case core.KindCodex:
		if bin, err = bins.Codex(st.Settings); err != nil {
			return fmt.Errorf("找不到 codex 命令：%w", err)
		}
		rest := o.Args
		if o.Resume != "" && !has(rest, "resume") {
			rest = append([]string{"resume", o.Resume}, rest...)
		}
		args = append(codexGatewayFlags(p.Addr), rest...)
		env = setEnv(codexEnv(os.Environ(), st.Settings, core.Account{}), codexGatewayKeyEnv, token)
	case core.KindGemini:
		dir := core.GatewayProfileDir(core.KindGemini)
		if err := gemini.EnsureProfile(st.Settings, dir); err != nil {
			return err
		}
		if err := gemini.UseAPIKeyAuth(dir); err != nil {
			return err
		}
		if bin, err = bins.Gemini(st.Settings); err != nil {
			return fmt.Errorf("找不到 gemini 命令：%w", err)
		}
		args = withResume(o.Args, o.Resume, "--resume")
		env = geminiEnv(os.Environ(), st.Settings, core.Account{ConfigDir: dir})
		env = withVars(env, gateway.Env(gateway.ProtoGemini, p.Addr, token))
		env = setEnv(env, "GEMINI_DEFAULT_AUTH_TYPE", "gemini-api-key")
	default:
		return fmt.Errorf("%s 不能走网关", o.Kind)
	}
	note("%s 走网关 http://%s，不经过号池。gcccode use-gateway off %s 恢复直连官方。", o.Kind, p.Addr, o.Kind)
	env = noProxyLoopback(env)
	if o.Kind == core.KindClaude {
		return runGatewayClaude(st, o, bin, env, note)
	}
	return foreground(bin, args, env, "", o.Dir)
}

// runGatewayClaude runs Claude Code on the gateway and, when the pooled
// account its session was bound to runs out of quota, starts a fresh session
// on another account with a handoff note. A conversation never moves to a
// different Claude login underneath Claude Code; only a new session does.
func runGatewayClaude(st *core.Store, o Options, bin string, env []string, note func(string, ...any)) error {
	base := append([]string{}, o.Args...)
	for hop := 0; hop < 4; hop++ {
		args, err := gatewayClaudeArgs(st, o, note)
		if err != nil {
			return err
		}
		started := time.Now()
		exitErr := foreground(bin, args, env, "", o.Dir)
		if exitErr == nil {
			return nil
		}
		next, ok := gatewayClaudeHandoff(st, o, started, note)
		if !ok {
			return exitErr
		}
		o.Resume = ""
		o.Args = append(append([]string{}, base...), next)
		if fresh, lerr := core.Load(); lerr == nil {
			st = fresh
		}
	}
	return nil
}

// gatewayClaudeHandoff decides whether to continue the just-ended Claude
// session on another pooled account, and writes the handoff file if so.
func gatewayClaudeHandoff(st *core.Store, o Options, started time.Time, note func(string, ...any)) (string, bool) {
	sess, ok := claude.Latest(st.Settings, o.Dir, started.Add(-2*time.Second), func(string) bool { return false })
	if !ok {
		return "", false
	}
	cur, err := core.Load()
	if err != nil {
		return "", false
	}
	owner := cur.State.SessionOwner[sess.ID]
	if owner == "" {
		return "", false
	}
	exhausted, _ := cur.Exhausted(owner, time.Now())
	a, _ := cur.Account(owner)
	if !exhausted && (a == nil || !a.Quarantined) {
		return "", false
	}
	ready := readyFn(cur)
	picked, _, perr := scheduler.Pick(cur, core.KindClaude, ready, []string{owner}, time.Now())
	if perr != nil {
		note("这个 Claude 号额度用完了，其余会员也都不可用。会话还在：gcccode resume %s", sess.ID)
		return "", false
	}
	if !cur.Settings.AutoContinue && !confirm("这个 Claude 号额度用完，用 "+label(picked.Account)+" 新开会话续接？") {
		note("会话还在。手动续接：gcccode claude --resume %s", sess.ID)
		return "", false
	}
	path, herr := handoff.WriteClaude(sess.Path, sess.ID)
	if herr != nil {
		return "", false
	}
	note("额度用完，改用 %s 新开会话续接。本地接力文件：%s", label(picked.Account), path)
	return handoff.Prompt + path, true
}

// gatewayClaudeArgs resumes a session directly unless a pooled subscription
// account owns it; that one gets a handoff note, so a subscription session is
// never continued on an API upstream.
func gatewayClaudeArgs(st *core.Store, o Options, note func(string, ...any)) ([]string, error) {
	if o.Resume == "" || has(o.Args, "--resume") || st.State.SessionOwner[o.Resume] == "" {
		return withResume(o.Args, o.Resume, "--resume"), nil
	}
	sess, ok := claude.FindSession(st.Settings, o.Resume)
	if !ok {
		return nil, fmt.Errorf("找不到会话 %s", o.Resume)
	}
	path, err := handoff.WriteClaude(sess.Path, o.Resume)
	if err != nil {
		return nil, err
	}
	note("这个会话属于号池里的订阅账号，不把它接到网关上游。本地记录在 %s", path)
	return append(append([]string{}, o.Args...), handoff.Prompt+path), nil
}

// codexGatewayFlags define a Codex model provider for this launch only;
// nothing is written to ~/.codex.
func codexGatewayFlags(addr string) []string {
	return []string{
		"-c", `model_provider="gcccode"`,
		"-c", `model_providers.gcccode.name="gcccode gateway"`,
		"-c", `model_providers.gcccode.base_url="http://` + addr + `/v1"`,
		"-c", `model_providers.gcccode.env_key="` + codexGatewayKeyEnv + `"`,
		"-c", `model_providers.gcccode.wire_api="responses"`,
	}
}

func withVars(env []string, vars []gateway.EnvVar) []string {
	for _, v := range vars {
		env = setEnv(env, v.Name, v.Value)
	}
	return env
}

// noProxyLoopback keeps requests to the local gateway off any HTTP proxy the
// environment sets.
func noProxyLoopback(env []string) []string {
	for _, key := range []string{"NO_PROXY", "no_proxy"} {
		cur := ""
		for _, e := range env {
			if k, v, _ := strings.Cut(e, "="); k == key {
				cur = v
			}
		}
		have := map[string]bool{}
		var parts []string
		for _, p := range strings.Split(cur, ",") {
			if p = strings.TrimSpace(p); p != "" {
				have[p] = true
				parts = append(parts, p)
			}
		}
		for _, h := range []string{"127.0.0.1", "localhost", "::1"} {
			if !have[h] {
				parts = append(parts, h)
			}
		}
		env = setEnv(env, key, strings.Join(parts, ","))
	}
	return env
}
