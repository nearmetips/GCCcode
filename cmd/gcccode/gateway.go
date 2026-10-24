package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/nearmetips/DailiCode/internal/claude"
	"github.com/nearmetips/DailiCode/internal/core"
	"github.com/nearmetips/DailiCode/internal/gateway"
)

const gatewayUsage = `用法：
  gcccode gateway start [--addr 127.0.0.1:3426]   后台启动网关
  gcccode gateway stop
  gcccode gateway status                          运行状态和每个上游的冷却情况
  gcccode gateway env [bash|fish] [openai|anthropic|gemini ...]
                                                  打印让 Agent 走网关的环境变量（eval 用）
  gcccode gateway serve [--addr ...]              前台运行（调试用）
  gcccode gateway provider add <名称> --url URL (--key KEY | --key-stdin)
          [--protocol openai|anthropic|gemini] [--responses] [--model 模型名]
  gcccode gateway provider add <名称> --subscription codex|claude [--accounts id,id]
          把号池里的 Codex Plus / Claude 会员做成上游，按额度自动换号
  gcccode gateway provider list|rm|enable|disable [名称]

地址写法和各家 SDK 一致：openai 带 /v1（https://api.openai.com/v1），
anthropic 不带（https://api.anthropic.com），gemini 不带（https://generativelanguage.googleapis.com）。
--responses 表示这个 openai 上游也支持 /v1/responses（Codex 用的接口）。
--subscription 让网关直接用你登录的订阅号轮换，无需 API Key。`

func cmdGateway(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("%s", gatewayUsage)
	}
	switch args[0] {
	case "start":
		return gatewayStart(args[1:])
	case "serve":
		return gatewayServe(args[1:])
	case "stop":
		p, err := gateway.Stop()
		if err != nil {
			return err
		}
		fmt.Printf("网关已停止（pid %d）\n", p.PID)
		return nil
	case "status":
		return gatewayStatus()
	case "env":
		return gatewayEnv(args[1:])
	case "provider", "providers":
		return gatewayProvider(args[1:])
	default:
		return fmt.Errorf("%s", gatewayUsage)
	}
}

func gatewayLoader() gateway.Loader {
	return func() (core.Gateway, error) {
		st, err := core.Load()
		if err != nil {
			return core.Gateway{}, err
		}
		return st.Settings.Gateway, nil
	}
}

// gatewayAddrFlag reads an optional --addr; the saved address, then the
// default, fill in when it is absent.
func gatewayAddrFlag(st *core.Store, args []string) (string, bool, error) {
	addr, given := "", false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--addr":
			if i+1 >= len(args) {
				return "", false, fmt.Errorf("--addr 后面要跟 host:port")
			}
			addr, given = args[i+1], true
			i++
		default:
			return "", false, fmt.Errorf("未知参数 %s", args[i])
		}
	}
	if addr == "" {
		addr = st.Settings.Gateway.Addr
	}
	if addr == "" {
		addr = core.DefaultGatewayAddr
	}
	return addr, given, nil
}

// ensureGatewayToken makes sure the local token exists, creating it once.
func ensureGatewayToken(addr string, saveAddr bool) (*core.Store, error) {
	return core.Mutate(func(s *core.Store) error {
		if s.Settings.Gateway.Token == "" {
			tok, err := gateway.NewToken()
			if err != nil {
				return err
			}
			s.Settings.Gateway.Token = tok
		}
		if saveAddr {
			s.Settings.Gateway.Addr = addr
		}
		return nil
	})
}

func gatewayStart(args []string) error {
	st, err := core.Load()
	if err != nil {
		return err
	}
	addr, given, err := gatewayAddrFlag(st, args)
	if err != nil {
		return err
	}
	if p, ok := gateway.Running(); ok {
		return fmt.Errorf("网关已经在运行：http://%s（pid %d）", p.Addr, p.PID)
	}
	if err := checkLoopbackAddr(addr); err != nil {
		return err
	}
	st, err = ensureGatewayToken(addr, given)
	if err != nil {
		return err
	}
	if len(st.Settings.Gateway.Providers) == 0 {
		fmt.Fprintln(os.Stderr, "提示：还没有上游。gcccode gateway provider add <名称> --url URL --key KEY")
	}
	p, err := spawnGateway(addr)
	if err != nil {
		return err
	}
	fmt.Printf("网关已启动 http://%s（pid %d）\n", p.Addr, p.PID)
	fmt.Println("让当前 shell 的 Agent 走它：eval \"$(gcccode gateway env)\"")
	if len(st.Settings.Gateway.Use) == 0 {
		fmt.Println("gcccode claude / codex / gemini 照旧直连官方、走号池；要让它们也走网关：gcccode use-gateway on")
	}
	return nil
}

// spawnGateway runs `gateway serve` in the background. Tests replace it.
var spawnGateway = func(addr string) (gateway.Process, error) {
	exe, err := os.Executable()
	if err != nil {
		return gateway.Process{}, err
	}
	return gateway.Spawn(exe, []string{"gateway", "serve", "--addr", addr}, addr)
}

// ensureGatewayRunning returns the running gateway, starting it on the saved
// address when it is not running.
func ensureGatewayRunning() (gateway.Process, error) {
	if p, ok := gateway.Running(); ok {
		return p, nil
	}
	st, err := core.Load()
	if err != nil {
		return gateway.Process{}, err
	}
	addr := gateway.Addr(st.Settings)
	if err := checkLoopbackAddr(addr); err != nil {
		return gateway.Process{}, err
	}
	if _, err := ensureGatewayToken(addr, false); err != nil {
		return gateway.Process{}, err
	}
	return spawnGateway(addr)
}

func checkLoopbackAddr(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("地址格式应为 host:port：%w", err)
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("网关只监听本机地址，拒绝 %s", host)
	}
	return nil
}

func gatewayServe(args []string) error {
	st, err := core.Load()
	if err != nil {
		return err
	}
	addr, _, err := gatewayAddrFlag(st, args)
	if err != nil {
		return err
	}
	if p, ok := gateway.Running(); ok && p.PID != os.Getpid() {
		return fmt.Errorf("网关已经在运行：http://%s（pid %d）", p.Addr, p.PID)
	}
	if _, err := ensureGatewayToken(addr, false); err != nil {
		return err
	}
	ln, err := gateway.ListenLoopback(addr)
	if err != nil {
		return err
	}
	if err := gateway.WritePID(ln.Addr().String()); err != nil {
		ln.Close()
		return err
	}
	defer gateway.RemovePID()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fmt.Fprintf(os.Stderr, "%s 网关监听 http://%s\n", time.Now().Format(time.RFC3339), ln.Addr())
	return gateway.New(gatewayLoader()).Serve(ctx, ln)
}

func gatewayStatus() error {
	st, err := core.Load()
	if err != nil {
		return err
	}
	gw := st.Settings.Gateway
	p, running := gateway.Running()
	if running {
		fmt.Printf("网关 运行中 http://%s  pid %d  启动于 %s\n", p.Addr, p.PID, p.Started.Format("2006-01-02 15:04:05"))
	} else {
		fmt.Println("网关 没有运行（gcccode gateway start）")
	}
	if len(gw.Providers) == 0 {
		fmt.Println("没有上游。gcccode gateway provider add <名称> --url URL --key KEY")
		return nil
	}
	live := map[string]gateway.ProviderStatus{}
	var subs []gateway.SubAccountStatus
	if running {
		if rep, err := gateway.FetchStatus(p.Addr, gw.Token); err == nil {
			for _, ps := range rep.Providers {
				live[ps.Name] = ps
			}
			subs = rep.Subscriptions
		} else {
			fmt.Fprintln(os.Stderr, "读不到网关状态：", err)
		}
	}
	for i, pr := range gw.Providers {
		if pr.Subscription != "" {
			who := "全部"
			if len(pr.Accounts) > 0 {
				who = strings.Join(pr.Accounts, ",")
			}
			state := "可用"
			if pr.Disabled {
				state = "已禁用"
			} else if !running {
				state = "待启动"
			}
			fmt.Printf("%d. %-14s 订阅:%-6s 号池账号=%-16s %s\n", i+1, pr.Name, pr.Subscription, who, state)
			for _, sa := range subs {
				if sa.Provider != pr.Name {
					continue
				}
				printSubAccount(sa)
			}
			continue
		}
		state := "可用"
		extra := ""
		switch {
		case pr.Disabled:
			state = "已禁用"
		case !running:
			state = "待启动"
		default:
			if ps, ok := live[pr.Name]; ok {
				extra = fmt.Sprintf("  成功 %d 次，失败 %d 次", ps.Served, ps.Failed)
				if ps.DownUntil > time.Now().Unix() {
					until := time.Unix(ps.DownUntil, 0)
					state = fmt.Sprintf("冷却中，%s 恢复（%s）", until.Format("15:04:05"), ps.Reason)
				}
			}
		}
		fmt.Printf("%d. %-14s %-9s %-34s %s%s\n", i+1, pr.Name, gateway.Protocol(pr), pr.BaseURL, state, extra)
	}
	return nil
}

func printSubAccount(sa gateway.SubAccountStatus) {
	name := sa.Label
	if name == "" {
		name = sa.Account
	}
	state := "可用"
	if sa.DownUntil > time.Now().Unix() {
		until := time.Unix(sa.DownUntil, 0)
		state = fmt.Sprintf("冷却中，%s 恢复（%s）", until.Format("15:04:05"), sa.Reason)
	} else if sa.Reason != "" {
		state = sa.Reason
	}
	fmt.Printf("     - %-16s 已用 %3.0f%%  成功 %d 失败 %d  %s\n", name, sa.UsedPct, sa.Served, sa.Failed, state)
}

// gatewayEnv prints the variables that point clients at the gateway. With no
// protocol named it covers every protocol the configured upstreams can answer.
func gatewayEnv(args []string) error {
	shell := "bash"
	var protos []string
	for _, a := range args {
		switch a {
		case "bash", "zsh", "sh", "fish":
			shell = a
		case gateway.ProtoOpenAI, gateway.ProtoAnthropic, gateway.ProtoGemini:
			protos = append(protos, a)
		default:
			return fmt.Errorf("gateway env 只认 bash、zsh、fish 和 openai、anthropic、gemini，不认 %s", a)
		}
	}
	st, err := core.Load()
	if err != nil {
		return err
	}
	addr := gateway.Addr(st.Settings)
	_, running := gateway.Running()
	st, err = ensureGatewayToken(addr, false)
	if err != nil {
		return err
	}
	if len(protos) == 0 {
		protos = gateway.Inbound(st.Settings.Gateway)
	}
	if len(protos) == 0 {
		protos = []string{gateway.ProtoOpenAI}
		fmt.Fprintln(os.Stderr, "还没有上游，先只打印 OpenAI 的变量。gcccode gateway provider add <名称> --url URL --key KEY")
	}
	var names []string
	for _, proto := range protos {
		if proto == gateway.ProtoAnthropic {
			for _, p := range st.Settings.Gateway.Providers {
				if gateway.Protocol(p) == gateway.ProtoOpenAI && p.Model == "" && !p.Disabled {
					fmt.Fprintf(os.Stderr, "提示：Claude Code 的请求翻译给 %s 时会带上原来的 claude-* 模型名，它不认的话，重新添加时加 --model。\n", p.Name)
				}
			}
		}
		for _, v := range gateway.Env(proto, addr, st.Settings.Gateway.Token) {
			names = append(names, v.Name)
			if shell == "fish" {
				fmt.Printf("set -gx %s %s\n", v.Name, v.Value)
			} else {
				fmt.Printf("export %s=%s\n", v.Name, v.Value)
			}
		}
	}
	if !running {
		fmt.Fprintln(os.Stderr, "网关还没启动：gcccode gateway start")
	}
	fmt.Fprintln(os.Stderr, "只对当前 shell 里直接运行的 Agent 有效；gcccode claude / codex / gemini 会去掉这些变量，要让它们走网关用 gcccode use-gateway on。")
	fmt.Fprintln(os.Stderr, "撤销：unset "+strings.Join(names, " "))
	return nil
}

const useGatewayUsage = `用法：
  gcccode use-gateway on [claude|codex|gemini ...]    这些官方客户端改走网关（不写名字就是所有接得上的）
  gcccode use-gateway off [claude|codex|gemini ...]   恢复直连官方、走号池（不写名字就是全部）
  gcccode use-gateway status`

// cmdUseGateway switches official clients between the account pool and the
// gateway. Only gcccode's own settings change; no client config file does.
func cmdUseGateway(args []string) error {
	if len(args) == 0 || args[0] == "status" {
		return useGatewayStatus()
	}
	if args[0] != "on" && args[0] != "off" {
		return fmt.Errorf("%s", useGatewayUsage)
	}
	on, kinds := args[0] == "on", args[1:]
	for _, k := range kinds {
		if !slices.Contains(gateway.Routable, k) && slices.Contains(core.Kinds, k) {
			return gateway.CanRoute(core.Gateway{}, k)
		}
		if !slices.Contains(gateway.Routable, k) {
			return fmt.Errorf("use-gateway 只认 claude、codex、gemini，不认 %s", k)
		}
	}
	st, err := core.Load()
	if err != nil {
		return err
	}
	if !on {
		return useGatewayOff(st, kinds)
	}
	if len(kinds) == 0 {
		for _, k := range gateway.Routable {
			if gateway.CanRoute(st.Settings.Gateway, k) == nil {
				kinds = append(kinds, k)
			}
		}
		if len(kinds) == 0 {
			return fmt.Errorf("网关里还没有能接官方客户端的上游。gcccode gateway provider add <名称> --url URL --key KEY [--protocol openai|anthropic|gemini]")
		}
	}
	for _, k := range kinds {
		if err := gateway.CanRoute(st.Settings.Gateway, k); err != nil {
			return err
		}
	}
	p, err := ensureGatewayRunning()
	if err != nil {
		return fmt.Errorf("启动网关失败：%w", err)
	}
	if _, err := core.Mutate(func(s *core.Store) error {
		s.Settings.Gateway.Use = mergeKinds(s.Settings.Gateway.Use, kinds, true)
		return nil
	}); err != nil {
		return err
	}
	fmt.Printf("%s 现在走网关 http://%s。\n", strings.Join(kinds, "、"), p.Addr)
	fmt.Println("gcccode " + strings.Join(kinds, "/") + " 启动时不再选号，限流和余额由网关在上游之间切换。")
	fmt.Println("用 --account 指定账号时仍走号池；恢复直连官方：gcccode use-gateway off")
	return nil
}

func useGatewayOff(st *core.Store, kinds []string) error {
	if len(kinds) == 0 {
		kinds = gateway.Routable
	}
	var was []string
	for _, k := range kinds {
		if gateway.Uses(st.Settings, k) {
			was = append(was, k)
		}
	}
	if len(was) == 0 {
		fmt.Printf("%s 本来就直连官方、走号池。\n", strings.Join(kinds, "、"))
		return nil
	}
	if _, err := core.Mutate(func(s *core.Store) error {
		s.Settings.Gateway.Use = mergeKinds(s.Settings.Gateway.Use, kinds, false)
		return nil
	}); err != nil {
		return err
	}
	fmt.Printf("%s 恢复直连官方、走号池。\n", strings.Join(was, "、"))
	if _, running := gateway.Running(); running {
		fmt.Println("网关还在运行（别的 Agent 可能在用）。要停：gcccode gateway stop")
	}
	return nil
}

// mergeKinds adds or removes kinds from use, keeping gateway.Routable order.
func mergeKinds(use, kinds []string, on bool) []string {
	var out []string
	for _, k := range gateway.Routable {
		if slices.Contains(kinds, k) {
			if on {
				out = append(out, k)
			}
		} else if slices.Contains(use, k) {
			out = append(out, k)
		}
	}
	return out
}

func useGatewayStatus() error {
	st, err := core.Load()
	if err != nil {
		return err
	}
	for _, line := range useGatewayLines(st) {
		fmt.Println(line)
	}
	return nil
}

// useGatewayLines says how each routable client launches, and why a client
// switched to the gateway would fail to start.
func useGatewayLines(st *core.Store) []string {
	var out []string
	for _, k := range gateway.Routable {
		if !gateway.Uses(st.Settings, k) {
			out = append(out, fmt.Sprintf("%-7s 直连官方（号池）", k))
			continue
		}
		line := fmt.Sprintf("%-7s 走网关", k)
		if err := gateway.CanRoute(st.Settings.Gateway, k); err != nil {
			line += "，但启动会失败：" + err.Error()
		}
		out = append(out, line)
	}
	return out
}

func gatewayProvider(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("%s", gatewayUsage)
	}
	switch args[0] {
	case "add":
		return providerAdd(args[1:])
	case "list", "ls":
		return providerList()
	case "rm", "remove", "enable", "disable":
		if len(args) != 2 {
			return fmt.Errorf("用法：gcccode gateway provider %s <名称>", args[0])
		}
		return providerChange(args[0], args[1])
	default:
		return fmt.Errorf("%s", gatewayUsage)
	}
}

func providerAdd(args []string) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return fmt.Errorf("用法：gcccode gateway provider add <名称> --url URL (--key KEY | --key-stdin) [--protocol openai|anthropic|gemini] [--responses] [--model 模型名]\n或：gcccode gateway provider add <名称> --subscription codex|claude [--accounts id,id]")
	}
	p := core.GatewayProvider{Name: args[0]}
	for i := 1; i < len(args); i++ {
		take := func() (string, error) {
			if i+1 >= len(args) {
				return "", fmt.Errorf("%s 需要一个值", args[i])
			}
			i++
			return args[i], nil
		}
		var err error
		switch args[i] {
		case "--url":
			p.BaseURL, err = take()
		case "--key":
			p.APIKey, err = take()
		case "--key-stdin":
			b, rerr := io.ReadAll(io.LimitReader(os.Stdin, 4096))
			if rerr != nil {
				return rerr
			}
			p.APIKey = strings.TrimSpace(string(b))
		case "--model":
			p.Model, err = take()
		case "--protocol":
			p.Protocol, err = take()
		case "--responses":
			p.Responses = true
		case "--subscription":
			p.Subscription, err = take()
		case "--accounts":
			v, aerr := take()
			if aerr != nil {
				return aerr
			}
			for _, id := range strings.Split(v, ",") {
				if id = strings.TrimSpace(id); id != "" {
					p.Accounts = append(p.Accounts, id)
				}
			}
		default:
			return fmt.Errorf("未知参数 %s", args[i])
		}
		if err != nil {
			return err
		}
	}
	p.BaseURL = strings.TrimSpace(p.BaseURL)
	p.Subscription = strings.TrimSpace(p.Subscription)
	if p.Protocol == gateway.ProtoOpenAI {
		p.Protocol = ""
	}
	if err := gateway.ValidateProvider(p); err != nil {
		return err
	}
	st, err := core.Load()
	if err != nil {
		return err
	}
	if p.Subscription != "" {
		if err := checkSubscriptionAdd(st, p); err != nil {
			return err
		}
	}
	_, err = core.Mutate(func(s *core.Store) error {
		for _, have := range s.Settings.Gateway.Providers {
			if have.Name == p.Name {
				return fmt.Errorf("已经有叫 %s 的上游。先 gcccode gateway provider rm %s", p.Name, p.Name)
			}
			if have.Subscription != "" && have.Subscription == p.Subscription {
				return fmt.Errorf("已经有一个 %s 订阅上游（%s），一种订阅只需要一个", p.Subscription, have.Name)
			}
		}
		s.Settings.Gateway.Providers = append(s.Settings.Gateway.Providers, p)
		return nil
	})
	if err != nil {
		return err
	}
	fmt.Printf("已添加上游 %s。列表顺序就是优先级；网关运行中时马上生效。\n", p.Name)
	if p.Subscription != "" {
		printSubscriptionNotice(p.Subscription)
	}
	return nil
}

// checkSubscriptionAdd makes sure the pooled accounts exist and, for Claude,
// that each will leave through its own egress: two subscriptions on one IP are
// how the vendor groups them.
func checkSubscriptionAdd(st *core.Store, p core.GatewayProvider) error {
	kind := p.Subscription
	accs := subscriptionAccounts(st, p)
	if len(accs) == 0 {
		return fmt.Errorf("号池里还没有 %s 账号。先 gcccode account add %s，或用 --accounts 指定已有账号", kind, kind)
	}
	for _, want := range p.Accounts {
		if _, ok := st.Account(want); !ok {
			return fmt.Errorf("没有这个账号：%s", want)
		}
	}
	if kind == core.KindClaude && len(accs) > 1 {
		var noProxy []string
		for _, a := range accs {
			if strings.TrimSpace(a.Proxy) == "" {
				noProxy = append(noProxy, a.ID)
			}
		}
		if len(noProxy) > 0 {
			return fmt.Errorf("多个 Claude 会员不能走同一出口，否则会被认成一组。先给这些号各设代理：%s\n  例如 gcccode account proxy %s http://127.0.0.1:7890", strings.Join(noProxy, "、"), noProxy[0])
		}
		for i := 0; i < len(accs); i++ {
			for j := i + 1; j < len(accs); j++ {
				if claude.SameEgress(accs[i].Proxy, accs[j].Proxy) {
					return fmt.Errorf("%s 和 %s 的代理出口相同。每个 Claude 会员要用各自的代理", accs[i].ID, accs[j].ID)
				}
			}
		}
	}
	return nil
}

// subscriptionAccounts are the pool accounts a subscription provider covers.
func subscriptionAccounts(st *core.Store, p core.GatewayProvider) []core.Account {
	allow := map[string]bool{}
	for _, id := range p.Accounts {
		allow[id] = true
	}
	var out []core.Account
	for _, a := range st.ByKind(p.Subscription) {
		if len(allow) == 0 || allow[a.ID] {
			out = append(out, a)
		}
	}
	return out
}

func printSubscriptionNotice(kind string) {
	switch kind {
	case core.KindCodex:
		fmt.Println("注意：这会用你登录的多个 ChatGPT/Codex 会员轮流发请求。多开、超量使用可能触犯 OpenAI 的条款，账号风险自负。")
	case core.KindClaude:
		fmt.Println("注意：这会用你登录的多个 Claude 会员轮流发请求，每个号必须用各自的代理出口。多开可能触犯 Anthropic 的条款，账号风险自负。")
	}
}

func providerList() error {
	st, err := core.Load()
	if err != nil {
		return err
	}
	if len(st.Settings.Gateway.Providers) == 0 {
		fmt.Println("没有上游。gcccode gateway provider add <名称> --url URL --key KEY")
		return nil
	}
	for i, p := range st.Settings.Gateway.Providers {
		state := "启用"
		if p.Disabled {
			state = "禁用"
		}
		if p.Subscription != "" {
			who := "全部"
			if len(p.Accounts) > 0 {
				who = strings.Join(p.Accounts, ",")
			}
			fmt.Printf("%d. %-14s 订阅:%-6s 号池账号=%-20s %s\n", i+1, p.Name, p.Subscription, who, state)
			continue
		}
		model := p.Model
		if model == "" {
			model = "（沿用请求里的）"
		}
		proto := gateway.Protocol(p)
		if p.Responses {
			proto += "+responses"
		}
		fmt.Printf("%d. %-14s %-18s %-34s key=%s  model=%s  %s\n", i+1, p.Name, proto, p.BaseURL, mask(p.APIKey), model, state)
	}
	return nil
}

func providerChange(op, name string) error {
	_, err := core.Mutate(func(s *core.Store) error {
		ps := s.Settings.Gateway.Providers
		for i := range ps {
			if ps[i].Name != name {
				continue
			}
			switch op {
			case "rm", "remove":
				s.Settings.Gateway.Providers = append(ps[:i:i], ps[i+1:]...)
			case "enable":
				ps[i].Disabled = false
			case "disable":
				ps[i].Disabled = true
			}
			return nil
		}
		return fmt.Errorf("没有叫 %s 的上游", name)
	})
	return err
}
