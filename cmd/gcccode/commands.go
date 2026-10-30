package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nearmetips/DailiCode/internal/agy"
	"github.com/nearmetips/DailiCode/internal/app"
	"github.com/nearmetips/DailiCode/internal/bins"
	"github.com/nearmetips/DailiCode/internal/claude"
	"github.com/nearmetips/DailiCode/internal/codex"
	"github.com/nearmetips/DailiCode/internal/core"
	"github.com/nearmetips/DailiCode/internal/gateway"
	"github.com/nearmetips/DailiCode/internal/gemini"
	"github.com/nearmetips/DailiCode/internal/grok"
	"github.com/nearmetips/DailiCode/internal/integrate"
	"github.com/nearmetips/DailiCode/internal/launcher"
	"github.com/nearmetips/DailiCode/internal/lcu"
	"github.com/nearmetips/DailiCode/internal/quota"
	"github.com/nearmetips/DailiCode/internal/relay"
	"github.com/nearmetips/DailiCode/internal/scheduler"
	"github.com/nearmetips/DailiCode/internal/tray"
	"github.com/nearmetips/DailiCode/internal/web"
)

// cmdStatusline prints the status line the user had before gcccode, if any,
// and gcccode's own line below it.
func cmdStatusline() {
	raw, _ := io.ReadAll(io.LimitReader(os.Stdin, 4<<20))
	st, err := core.Load()
	if err != nil {
		fmt.Println("gcccode")
		return
	}
	theirs := integrate.RunPrevious(st.Settings, raw)
	line := statusLine(st, raw)
	if theirs != nil {
		if out := <-theirs; out != "" {
			fmt.Println(out)
		}
	}
	fmt.Println(line)
}

func statusLine(st *core.Store, raw []byte) string {
	in, err := claude.ParseStatusInput(bytes.NewReader(raw))
	if err != nil {
		return "gcccode"
	}
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		dir = core.ClaudeHome(st.Settings)
	}
	acc, ok := claude.AccountByDir(st, dir)
	name := "claude"
	if filepath.Clean(dir) == filepath.Clean(core.GatewayProfileDir(core.KindClaude)) {
		name = "网关"
	}
	id := ""
	if ok {
		id = acc.ID
		if acc.Label != "" {
			name = acc.Label
		} else {
			name = acc.ID
		}
	}
	now := time.Now()
	q, has := claude.QuotaFrom(id, in, now)
	launch := os.Getenv(launcher.LaunchEnv)
	record := in.SessionID != "" && (st.State.SessionOwner[in.SessionID] != id ||
		launch != "" && st.State.Launches[launch].SessionID != in.SessionID)
	if id != "" && (has || record) {
		_, _ = core.MutateTimeout(200*time.Millisecond, func(s *core.Store) error {
			if in.SessionID != "" {
				s.State.SessionOwner[in.SessionID] = id
				if cur, ok := s.State.Occupied[id]; ok {
					cur.SessionID = in.SessionID
					s.State.Occupied[id] = cur
				}
				if launch != "" {
					s.State.Launches[launch] = core.Launch{SessionID: in.SessionID, Transcript: in.TranscriptPath, At: now}
				}
			}
			if !has {
				return nil
			}
			s.Quota[id] = q
			quota.MaybeWarn(s, id, q, now)
			for _, w := range []*core.Window{q.FiveHour, q.Weekly} {
				if w != nil && w.UsedPct >= s.Settings.LimitPct && w.ResetAt > 0 {
					s.State.Cooldown[id] = w.ResetAt
				}
			}
			return nil
		})
	}
	next := ""
	var all []scheduler.Status
	if st2, err := core.Load(); err == nil {
		ready := accountReady(st2)
		next = scheduler.NextName(st2, core.KindClaude, ready, []string{id}, now)
		all = scheduler.Evaluate(st2, core.KindClaude, ready, now)
	}
	line := scheduler.FormatLine(name, q, has, next, now)
	if has {
		over := false
		for _, w := range []*core.Window{q.FiveHour, q.Weekly} {
			if w != nil && w.UsedPct >= st.Settings.LimitPct {
				over = true
			}
		}
		switch {
		case over && next != "":
			line += " · 退出后可换号续接"
		case over:
			if s, ok := scheduler.Soonest(all); ok {
				line += " · 号都用完了，" + scheduler.Display(s.Account) + " " + scheduler.FormatReset(s.Until, now) + " 恢复"
			}
		}
	}
	return line
}

func cmdIntegrate(args []string) error {
	if len(args) > 0 {
		if args[0] != "off" {
			return fmt.Errorf("用法：gcccode integrate [off]")
		}
		removed, err := integrate.RemoveStatusLine()
		if err != nil {
			return err
		}
		if removed {
			fmt.Println("已从 Claude Code 状态栏撤下 gcccode，原来的状态栏已放回。")
		} else {
			fmt.Println("Claude Code 状态栏里本来就没有 gcccode。")
		}
		return nil
	}
	prev, err := integrate.InstallStatusLine()
	if err != nil {
		return err
	}
	fmt.Println("已写入 Claude Code 状态栏。新开一个会话就能看到额度。")
	if prev != "" {
		fmt.Printf("原来的状态栏照常显示，gcccode 的额度在它下面一行：%s\n", prev)
	}
	fmt.Println("撤销：gcccode integrate off")
	fmt.Println("如果希望直接敲 claude / codex 也走号池，把下面这行加进 shell 配置：")
	fmt.Println("  eval \"$(gcccode shell-init)\"")
	return nil
}

func shellInit(shell string) string { return integrate.ShellInit(shell) }

func cmdResume(args []string) error {
	st, err := core.Load()
	if err != nil {
		return err
	}
	var id string
	var rest []string
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		latest, err := latestHere(st)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "续接这个目录最近的会话（%s · %s）：%s\n", latest.Kind, latest.Mod.Local().Format("01-02 15:04"), latest.Title)
		id, rest = latest.ID, args
	} else {
		id, rest = args[0], args[1:]
	}
	if sess, ok := claude.FindSession(st.Settings, id); ok {
		dir, err := sessionDir(claude.SessionCwd(sess.Path))
		if err != nil {
			return err
		}
		return launcher.Run(launcher.Options{Kind: core.KindClaude, Resume: id, Args: rest, Dir: dir})
	}
	if sess, ok := codex.FindSession(st.Settings, id); ok {
		dir, err := sessionDir(sess.Cwd)
		if err != nil {
			return err
		}
		return launcher.Run(launcher.Options{Kind: core.KindCodex, Resume: id, Args: rest, Dir: dir})
	}
	if sess, ok := gemini.FindSession(st.Settings, "", id); ok {
		dir, err := sessionDir(sess.Cwd)
		if err != nil {
			return err
		}
		acc := ""
		if owner := st.State.SessionOwner[id]; owner != "" {
			acc = owner
		} else if a, ok := gemini.AccountByDir(st, os.Getenv("GEMINI_CONFIG_DIR")); ok {
			acc = a.ID
		}
		return launcher.Run(launcher.Options{Kind: core.KindGemini, Account: acc, Resume: id, Args: rest, Dir: dir})
	}
	for _, a := range st.ByKind(core.KindGemini) {
		if sess, ok := gemini.FindSession(st.Settings, a.ConfigDir, id); ok {
			dir, err := sessionDir(sess.Cwd)
			if err != nil {
				return err
			}
			return launcher.Run(launcher.Options{Kind: core.KindGemini, Account: a.ID, Resume: id, Args: rest, Dir: dir})
		}
	}
	if sess, ok := grok.FindSession(st.Settings, "", id); ok {
		dir, err := sessionDir(sess.Cwd)
		if err != nil {
			return err
		}
		acc := st.State.SessionOwner[id]
		if acc == "" {
			if a, ok := grok.AccountByDir(st, core.GrokHome(st.Settings)); ok {
				acc = a.ID
			}
		}
		return launcher.Run(launcher.Options{Kind: core.KindGrok, Account: acc, Resume: id, Args: rest, Dir: dir})
	}
	for _, a := range st.ByKind(core.KindGrok) {
		if sess, ok := grok.FindSession(st.Settings, a.ConfigDir, id); ok {
			dir, err := sessionDir(sess.Cwd)
			if err != nil {
				return err
			}
			return launcher.Run(launcher.Options{Kind: core.KindGrok, Account: a.ID, Resume: id, Args: rest, Dir: dir})
		}
	}
	if sess, ok := agy.FindSession(st.Settings, "", id); ok {
		dir, err := sessionDir(sess.Cwd)
		if err != nil {
			return err
		}
		if _, ok := agy.AccountByDir(st, core.AgyHome(st.Settings)); !ok {
			return fmt.Errorf("Antigravity 还不在号池里。先 gcccode account import agy")
		}
		return launcher.Run(launcher.Options{Kind: core.KindAgy, Resume: id, Args: rest, Dir: dir})
	}
	return fmt.Errorf("本地没有找到会话 %s", id)
}

// sessionDir is where a resumed session must run: the directory it started
// in. Claude only finds a session to resume from that directory.
func sessionDir(recorded string) (string, error) {
	if recorded == "" {
		return "", nil
	}
	if fi, err := os.Stat(recorded); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("这个会话是在 %s 里开的，这个目录已经不在了", recorded)
	}
	if cwd, err := os.Getwd(); err != nil || !core.SamePath(cwd, recorded) {
		fmt.Fprintf(os.Stderr, "到会话原来的目录里续接：%s\n", recorded)
	}
	return recorded, nil
}

func cmdStrategy(args []string) error {
	if len(args) == 0 {
		st, err := core.Load()
		if err != nil {
			return err
		}
		fmt.Println(st.Settings.Strategy)
		return nil
	}
	switch args[0] {
	case "smart", "pace", "manual":
	default:
		return fmt.Errorf("策略只能是 smart、pace 或 manual")
	}
	_, err := core.Mutate(func(s *core.Store) error {
		s.Settings.Strategy = args[0]
		return nil
	})
	return err
}

func cmdContinue(args []string) error {
	if len(args) == 0 {
		st, err := core.Load()
		if err != nil {
			return err
		}
		fmt.Println(map[bool]string{true: "on", false: "off"}[st.Settings.AutoContinue])
		return nil
	}
	on := args[0] == "on" || args[0] == "true"
	if args[0] != "on" && args[0] != "off" {
		return fmt.Errorf("用法：gcccode continue on|off")
	}
	_, err := core.Mutate(func(s *core.Store) error {
		s.Settings.AutoContinue = on
		return nil
	})
	return err
}

func cmdRelay(args []string) error {
	if len(args) == 0 {
		st, err := core.Load()
		if err != nil {
			return err
		}
		r := st.Settings.Relay
		fmt.Printf("enabled=%v ready=%v name=%s\n", r.Enabled, relay.Ready(r), r.Name)
		fmt.Printf("anthropic=%s\nopenai=%s\n", or(r.AnthropicURL, "（未设置）"), or(r.OpenAIURL, "（未设置）"))
		fmt.Printf("token=%s\n", mask(r.Token))
		return nil
	}
	switch args[0] {
	case "enable", "disable":
		on := args[0] == "enable"
		_, err := core.Mutate(func(s *core.Store) error {
			s.Settings.Relay.Enabled = on
			return nil
		})
		return err
	case "set":
		return relaySet(args[1:])
	default:
		return fmt.Errorf("用法：gcccode relay set --anthropic URL --openai URL --token TOKEN")
	}
}

func relaySet(args []string) error {
	var anthropic, openai, token, name string
	for i := 0; i < len(args); i++ {
		take := func() (string, error) {
			if i+1 >= len(args) {
				return "", fmt.Errorf("%s 需要一个值", args[i])
			}
			i++
			return args[i], nil
		}
		var err error
		switch args[i] {
		case "--anthropic":
			anthropic, err = take()
		case "--openai":
			openai, err = take()
		case "--token":
			token, err = take()
		case "--name":
			name, err = take()
		default:
			return fmt.Errorf("未知参数 %s", args[i])
		}
		if err != nil {
			return err
		}
	}
	_, err := core.Mutate(func(s *core.Store) error {
		if anthropic != "" {
			s.Settings.Relay.AnthropicURL = anthropic
		}
		if openai != "" {
			s.Settings.Relay.OpenAIURL = openai
		}
		if token != "" {
			s.Settings.Relay.Token = token
		}
		if name != "" {
			s.Settings.Relay.Name = name
		}
		if s.Settings.Relay.Name == "" {
			s.Settings.Relay.Name = "gcccode"
		}
		s.Settings.Relay.Enabled = true
		return nil
	})
	if err != nil {
		return err
	}
	fmt.Println("中转已保存，并在订阅账号都不可用时启用。")
	return nil
}

// cmdApp shows the panel in its own window. The panel listens on a port of
// its own, so a `gcccode web` already running is left alone.
func cmdApp(args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("用法：gcccode app")
	}
	app.WidenPath()
	if !app.Supported {
		return cmdWeb(nil)
	}
	ln, err := web.Listen("127.0.0.1:0")
	if err != nil {
		return err
	}
	web.App = true
	go func() {
		if err := web.ServeOn(ln); err != nil {
			fmt.Fprintln(os.Stderr, "gcccode:", err)
		}
	}()
	return app.Run("http://"+ln.Addr().String(), "GCC Code")
}

func cmdWeb(args []string) error {
	st, err := core.Load()
	if err != nil {
		return err
	}
	addr := web.Addr(st.Settings)
	open := true
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--addr":
			if i+1 >= len(args) {
				return fmt.Errorf("--addr 需要一个地址，例如 127.0.0.1:4725")
			}
			i++
			addr = args[i]
		case "--no-open":
			open = false
		default:
			return fmt.Errorf("用法：gcccode web [--addr 127.0.0.1:4725] [--no-open]")
		}
	}
	if web.Running(addr) {
		fmt.Println("面板已经在运行：http://" + addr)
		if open {
			web.OpenBrowser("http://" + addr)
		}
		return nil
	}
	return web.Serve(addr, open)
}

func cmdTray() error { return tray.Run() }

func cmdDoctor() error {
	st, err := core.Load()
	if err != nil {
		return err
	}
	fmt.Println("gcccode", core.Version)
	fmt.Println("配置目录", core.Root())
	if p, err := bins.Claude(st.Settings); err != nil {
		fmt.Println("claude    没找到。安装 Claude Code 后再 gcccode account add claude")
	} else {
		fmt.Println("claude   ", p)
	}
	if p, err := bins.Codex(st.Settings); err != nil {
		fmt.Println("codex     没找到。安装 Codex CLI 或 ChatGPT 桌面版")
	} else {
		fmt.Println("codex    ", p)
	}
	if p, err := bins.Gemini(st.Settings); err != nil {
		fmt.Println("gemini    没找到。安装 Gemini CLI 后再 gcccode account add gemini")
	} else {
		fmt.Println("gemini   ", p)
	}
	if p, err := bins.Grok(st.Settings); err != nil {
		fmt.Println("grok      没找到。安装 Grok CLI 后再 gcccode account add grok")
	} else {
		fmt.Println("grok     ", p)
	}
	if p, err := bins.Agy(st.Settings); err != nil {
		fmt.Println("agy       没找到。安装 Antigravity CLI 后再 gcccode account add agy")
	} else {
		fmt.Println("agy      ", p)
	}
	lcuMain := false
	if p, ok := lcu.Command(); ok {
		fmt.Println("lcu      ", p, lcu.Version(p))
		lcuMain = lcu.InspectClaude(claude.MainConfigJSON(st.Settings), filepath.Join(core.ClaudeHome(st.Settings), "settings.json")).Server
		if !lcuMain {
			fmt.Println("          还没注册到 Claude Code。gcccode lcu setup")
		}
	} else {
		fmt.Println("lcu       没装。要 computer use 的话：gcccode lcu install")
	}
	if integrate.Installed(st.Settings) {
		line := "状态栏    已接入 gcccode"
		if prev := integrate.PreviousCommand(st.Settings); prev != "" {
			line += "，原来的状态栏也在：" + prev
		}
		fmt.Println(line)
	} else if len(st.ByKind(core.KindClaude)) > 0 {
		fmt.Println("状态栏    没接入 gcccode，Claude 的额度会一直是未知，选号只能盲选。运行 gcccode integrate")
	}
	if claude.RelayInSettings(st.Settings) {
		fmt.Println("注意：~/.claude/settings.json 里有中转或 API Key。号池里的 Claude 账号会去掉这些，改走各自的会员登录。")
	}
	if os.Getenv(claude.TokenEnv) != "" {
		fmt.Println("注意：这个 shell 里 export 了 " + claude.TokenEnv + "。直接敲 claude 会一直用这一个号；gcccode 启动时会换成各号自己的 token。建议从 ~/.zshrc 之类的配置里删掉，换号交给 gcccode。")
	}
	now := time.Now()
	ready := accountReady(st)
	for _, kind := range core.Kinds {
		rows := scheduler.Evaluate(st, kind, ready, now)
		if line := scheduler.NoneUsable(rows, now); line != "" {
			fmt.Printf("%-16s %s\n", kind, line)
		}
		for _, row := range rows {
			fmt.Printf("%-16s %s\n", row.Account.ID, row.Reason)
			if row.Account.Kind == core.KindClaude {
				if claude.UsesToken(row.Account) {
					fmt.Printf("               %s\n", tokenLine(row.Account, now))
				} else if !claude.IsMainHome(st.Settings, row.Account.ConfigDir) {
					fmt.Printf("               钥匙串条目 %s\n", claude.KeychainService(row.Account.ConfigDir))
				}
				if bad := claude.LinkReport(st.Settings, row.Account.ConfigDir); len(bad) > 0 {
					fmt.Printf("               这些条目没有连到主目录，换号可能看不到旧会话：%s\n", strings.Join(bad, ", "))
				}
				if state, ok := accountLCUState(st, row.Account); lcuMain && !ok {
					fmt.Printf("               LCU %s\n", state)
				}
			}
			if row.Account.Kind == core.KindClaude {
				if msg := claude.EgressProblem(st.Settings, st.Accounts, row.Account.ID); msg != "" {
					fmt.Printf("               出口有问题：%s\n", msg)
				}
			}
			if row.Account.Kind == core.KindClaude {
				if p := claude.EffectiveProxy(st.Settings, row.Account); p != "" {
					from := ""
					if p != strings.TrimSpace(row.Account.Proxy) {
						from = "（来自 ~/.claude/settings.json）"
					}
					fmt.Printf("               出口代理 %s%s\n", p, from)
				}
			} else if row.Account.Proxy != "" {
				fmt.Printf("               出口代理 %s\n", row.Account.Proxy)
			}
		}
	}
	for _, line := range claude.DeviceClashes(st.Settings, st.Accounts) {
		fmt.Println("Claude 设备标识重复：", line, "。下次 gcccode claude 会把后加的那个清掉，让官方客户端重新生成。")
	}
	if procs := codex.Running(); len(procs) > 0 {
		fmt.Println("Codex 正在运行，现在不能换 ChatGPT 号：")
		for _, p := range procs {
			fmt.Println(" ", p)
		}
	}
	r := st.Settings.Relay
	fmt.Printf("中转兜底 enabled=%v ready=%v\n", r.Enabled, relay.Ready(r))
	for _, line := range gatewayDoctor(st, os.Getenv) {
		fmt.Println(line)
	}
	fmt.Println("策略", st.Settings.Strategy, " 自动续接", map[bool]string{true: "on", false: "off"}[st.Settings.AutoContinue])
	return nil
}

// tokenLine describes a setup-token account's token and when to renew it.
func tokenLine(a core.Account, now time.Time) string {
	left := claude.TokenDaysLeft(a, now)
	switch {
	case left < 0:
		return "长期 token 已过期。重新拿一个：gcccode account login " + a.ID
	case left <= 30:
		return fmt.Sprintf("长期 token 还剩 %d 天，该续期了：gcccode account login %s", left, a.ID)
	}
	return fmt.Sprintf("长期 token，%s 存的，还剩 %d 天", a.TokenAt.Local().Format("2006-01-02"), left)
}

// gatewayDoctor describes the optional gateway for `gcccode doctor`, and says
// so when this shell's base-URL variables point at it: clients started
// directly then use the gateway's upstreams instead of the pool, which is the
// "gateway or official direct, not both" rule.
func gatewayDoctor(st *core.Store, getenv func(string) string) []string {
	gw := st.Settings.Gateway
	p, running := gateway.Running()
	var bases, all []string
	for _, proto := range []string{gateway.ProtoOpenAI, gateway.ProtoAnthropic, gateway.ProtoGemini} {
		vars := gateway.Env(proto, "", "")
		if gateway.PointsAt(st.Settings, getenv(vars[0].Name)) {
			bases = append(bases, vars[0].Name)
			for _, v := range vars {
				all = append(all, v.Name)
			}
		}
	}
	if !running && len(gw.Providers) == 0 && gw.Addr == "" && len(bases) == 0 && len(gw.Use) == 0 {
		return nil
	}
	var out []string
	if running {
		out = append(out, fmt.Sprintf("网关     运行中 http://%s，%d 个上游", p.Addr, len(gw.Providers)))
	} else {
		out = append(out, fmt.Sprintf("网关     没有运行，%d 个上游（gcccode gateway start）", len(gw.Providers)))
	}
	if len(gw.Use) > 0 {
		for _, line := range useGatewayLines(st) {
			out = append(out, "  "+line)
		}
		out = append(out, "  走网关的客户端不选号、不换号；用 --account 指定账号时仍走号池。gcccode use-gateway off 恢复直连官方")
	}
	out = append(out, subscriptionDoctor(st)...)
	if len(bases) > 0 {
		which := strings.Join(bases, "、")
		if running {
			out = append(out, "注意：这个 shell 的 "+which+" 指向网关，直接运行的 Agent 走网关上游、不走号池；gcccode claude / codex / gemini 不看这些变量，走不走网关由 use-gateway 决定。恢复：unset "+strings.Join(all, " "))
		} else {
			out = append(out, "注意：这个 shell 的 "+which+" 指向网关，但网关没在运行，直接运行的 Agent 会连不上。gcccode gateway start，或 unset "+strings.Join(all, " "))
		}
	}
	return out
}

// subscriptionDoctor flags subscription upstreams and their account-sharing risks.
func subscriptionDoctor(st *core.Store) []string {
	var out []string
	for _, p := range st.Settings.Gateway.Providers {
		if p.Subscription == "" || p.Disabled {
			continue
		}
		accs := subscriptionAccounts(st, p)
		who := "全部"
		if len(p.Accounts) > 0 {
			who = strings.Join(p.Accounts, ",")
		}
		out = append(out, fmt.Sprintf("订阅     %s 轮换 %d 个 %s 号（%s）；多开可能触犯条款，账号风险自负", p.Name, len(accs), p.Subscription, who))
		if p.Subscription == core.KindClaude {
			var noProxy []string
			for _, a := range accs {
				if strings.TrimSpace(a.Proxy) == "" {
					noProxy = append(noProxy, a.ID)
				}
			}
			for i := 0; i < len(accs); i++ {
				for j := i + 1; j < len(accs); j++ {
					if claude.SameEgress(accs[i].Proxy, accs[j].Proxy) {
						out = append(out, fmt.Sprintf("  注意：%s 和 %s 代理出口相同，会被 Anthropic 认成一组。各设各的代理", accs[i].ID, accs[j].ID))
					}
				}
			}
			if len(noProxy) > 0 {
				out = append(out, "  注意：这些 Claude 号还没设代理，多号同出口有风险："+strings.Join(noProxy, "、"))
			}
		}
	}
	return out
}

func mask(token string) string {
	if token == "" {
		return "（未设置）"
	}
	if len(token) <= 6 {
		return "已设置"
	}
	return token[:3] + "…" + token[len(token)-3:]
}
