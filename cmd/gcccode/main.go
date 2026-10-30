package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/nearmetips/DailiCode/internal/agy"
	"github.com/nearmetips/DailiCode/internal/app"
	"github.com/nearmetips/DailiCode/internal/browser"
	"github.com/nearmetips/DailiCode/internal/claude"
	"github.com/nearmetips/DailiCode/internal/codex"
	"github.com/nearmetips/DailiCode/internal/core"
	"github.com/nearmetips/DailiCode/internal/gemini"
	"github.com/nearmetips/DailiCode/internal/grok"
	"github.com/nearmetips/DailiCode/internal/launcher"
)

func main() {
	launcher.EnsureGateway = ensureGatewayRunning
	args := os.Args[1:]
	if id := os.Getenv(browser.ForEnv); id != "" && len(args) == 1 && isURL(args[0]) {
		// A client signing in opened this link with gcccode as its BROWSER.
		if err := openAccountBrowser(id, args[0]); err != nil {
			die(err)
		}
		return
	}
	if len(args) == 1 && strings.HasPrefix(args[0], "-psn_") {
		args = nil // Finder's process serial number on older macOS
	}
	if len(args) == 0 && app.Bundled() {
		// Double-clicked in Finder or started from the Dock.
		if err := cmdApp(nil); err != nil {
			die(err)
		}
		return
	}
	if len(args) == 0 {
		if err := cmdAccounts(); err != nil {
			die(err)
		}
		fmt.Fprintln(os.Stderr, "\n"+shortHelp())
		return
	}
	var err error
	switch args[0] {
	case "app":
		err = cmdApp(args[1:])
	case "help", "-h", "--help":
		fmt.Println(longHelp())
	case "version", "-v", "--version":
		fmt.Println("gcccode", core.Version)
	case "accounts":
		err = cmdAccounts()
	case "account":
		err = cmdAccount(args[1:])
	case "use":
		err = cmdUse(args[1:])
	case "claude", "codex", "gemini", "grok", "agy":
		err = cmdLaunch(args[0], args[1:])
	case "resume":
		err = cmdResume(args[1:])
	case "sessions":
		err = cmdSessions(args[1:])
	case "statusline":
		cmdStatusline()
	case "integrate":
		err = cmdIntegrate(args[1:])
	case "shell-init":
		shell := "bash"
		if len(args) > 1 {
			shell = args[1]
		}
		fmt.Print(shellInit(shell))
	case "doctor":
		err = cmdDoctor()
	case "web":
		err = cmdWeb(args[1:])
	case "tray":
		err = cmdTray()
	case "strategy":
		err = cmdStrategy(args[1:])
	case "relay":
		err = cmdRelay(args[1:])
	case "continue":
		err = cmdContinue(args[1:])
	case "lcu":
		err = cmdLCU(args[1:])
	case "gateway":
		err = cmdGateway(args[1:])
	case "use-gateway":
		err = cmdUseGateway(args[1:])
	default:
		err = fmt.Errorf("未知命令 %q\n\n%s", args[0], shortHelp())
	}
	if err != nil {
		die(err)
	}
}

// die reports err. The official client has already printed why it exited,
// so only its exit code is passed on.
func die(err error) {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		code := exit.ExitCode()
		if code < 0 {
			code = 1
		}
		os.Exit(code)
	}
	fmt.Fprintln(os.Stderr, "gcccode:", err)
	os.Exit(1)
}

func cmdLaunch(kind string, args []string) error {
	o := launcher.Options{Kind: kind}
	rest, err := parseLaunchFlags(&o, args)
	if err != nil {
		return err
	}
	if launcher.Management(kind, rest) {
		if kind == core.KindCodex && len(rest) == 1 && rest[0] == "login" {
			return addCodex("")
		}
		if kind == core.KindGrok && len(rest) == 1 && rest[0] == "login" && o.Account == "" {
			return addGrok("")
		}
		if kind == core.KindGemini && ((len(rest) == 1 && rest[0] == "login") || (len(rest) == 2 && rest[0] == "auth" && rest[1] == "login")) && o.Account == "" {
			return addGemini("")
		}
		return launcher.Passthrough(kind, o.Account, rest)
	}
	if o.Resume == "" {
		rest = takeResume(&o, rest)
	}
	o.Args = rest
	return launcher.Run(o)
}

// parseLaunchFlags reads gcccode's own flags in front of the client's. For
// Claude, the client's resume flags are held back for takeResume, so they
// may come before --account too.
func parseLaunchFlags(o *launcher.Options, args []string) ([]string, error) {
	var held []string
	for len(args) > 0 {
		a := args[0]
		switch {
		case a == "--account":
			if len(args) < 2 {
				return nil, fmt.Errorf("--account 需要一个账号 id")
			}
			o.Account = args[1]
			args = args[2:]
		case strings.HasPrefix(a, "--account="):
			o.Account = strings.TrimPrefix(a, "--account=")
			args = args[1:]
		case a == "--relay":
			o.Relay = true
			args = args[1:]
		case o.Kind == core.KindCodex && a == "--resume":
			if len(args) < 2 || strings.HasPrefix(args[1], "-") {
				return nil, fmt.Errorf("--resume 需要会话 id")
			}
			o.Resume = args[1]
			args = args[2:]
		case (o.Kind == core.KindClaude || o.Kind == core.KindGemini || o.Kind == core.KindGrok) && (a == "-c" || a == "--continue" || strings.HasPrefix(a, "--resume=")):
			held = append(held, a)
			args = args[1:]
		case (o.Kind == core.KindClaude || o.Kind == core.KindGemini || o.Kind == core.KindGrok) && (a == "-r" || a == "--resume"):
			n := 1
			if len(args) > 1 && !strings.HasPrefix(args[1], "-") {
				n = 2
			}
			held = append(held, args[:n]...)
			args = args[n:]
		case o.Kind == core.KindAgy && (a == "-c" || a == "--continue" || strings.HasPrefix(a, "--conversation=")):
			held = append(held, a)
			args = args[1:]
		case o.Kind == core.KindAgy && a == "--conversation":
			n := 1
			if len(args) > 1 && !strings.HasPrefix(args[1], "-") {
				n = 2
			}
			held = append(held, args[:n]...)
			args = args[n:]
		default:
			return append(held, args...), nil
		}
	}
	return held, nil
}

// takeResume moves the session the client's own resume flags point at into
// o.Resume, so the launcher can resume it on the account that started it, or
// hand it off to another account instead of resuming the cloud session there.
// A value that is not a local session (a picker search term) stays as is.
func takeResume(o *launcher.Options, args []string) []string {
	st, err := core.Load()
	if err != nil {
		return args
	}
	if o.Kind == core.KindCodex {
		if len(args) >= 2 && args[0] == "resume" && !strings.HasPrefix(args[1], "-") {
			if _, ok := codex.FindSession(st.Settings, args[1]); ok {
				o.Resume = args[1]
				return args[2:]
			}
		}
		return args
	}
	if o.Kind == core.KindAgy {
		return takeAgyResume(st, o, args)
	}
	found := func(id string) bool {
		switch o.Kind {
		case core.KindGemini:
			_, ok := gemini.FindSession(st.Settings, "", id)
			return ok
		case core.KindGrok:
			_, ok := grok.FindSession(st.Settings, "", id)
			return ok
		default:
			_, ok := claude.FindSession(st.Settings, id)
			return ok
		}
	}
	latestHere := func() (string, bool) {
		dir, err := os.Getwd()
		if err != nil {
			return "", false
		}
		switch o.Kind {
		case core.KindGemini:
			sess, ok := gemini.Latest(st.Settings, "", dir, time.Time{})
			return sess.ID, ok
		case core.KindGrok:
			sess, ok := grok.Latest(st.Settings, "", dir, time.Time{})
			return sess.ID, ok
		default:
			sess, ok := claude.Latest(st.Settings, dir, time.Time{}, nil)
			return sess.ID, ok
		}
	}
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		if o.Resume != "" {
			out = append(out, a)
			continue
		}
		switch {
		case a == "-c" || a == "--continue":
			if id, ok := latestHere(); ok {
				o.Resume = id
				continue
			}
		case (a == "-r" || a == "--resume") && i+1 < len(args) && found(args[i+1]):
			o.Resume = args[i+1]
			i++
			continue
		case strings.HasPrefix(a, "--resume=") && found(strings.TrimPrefix(a, "--resume=")):
			o.Resume = strings.TrimPrefix(a, "--resume=")
			continue
		}
		out = append(out, a)
	}
	return out
}

// takeAgyResume moves the conversation agy's own flags point at into
// o.Resume, so the pool records which account it ran on. A -c with no
// indexed conversation here stays for agy to resolve itself.
func takeAgyResume(st *core.Store, o *launcher.Options, args []string) []string {
	found := func(id string) bool {
		_, ok := agy.FindSession(st.Settings, "", id)
		return ok
	}
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		if o.Resume == "" {
			id := ""
			switch {
			case a == "-c" || a == "--continue":
				if dir, err := os.Getwd(); err == nil {
					if recent := agy.Recent(st.Settings, "", dir, 1); len(recent) > 0 {
						id = recent[0].ID
					}
				}
			case a == "--conversation" && i+1 < len(args) && found(args[i+1]):
				id = args[i+1]
				i++
			case strings.HasPrefix(a, "--conversation=") && found(strings.TrimPrefix(a, "--conversation=")):
				id = strings.TrimPrefix(a, "--conversation=")
			}
			if id != "" {
				o.Resume = id
				continue
			}
		}
		out = append(out, a)
	}
	return out
}

func shortHelp() string {
	return `命令：
  gcccode accounts              查看每个号的额度和状态
  gcccode account add claude|codex|gemini|grok|agy
  gcccode claude|codex|gemini|grok|agy   选一个有额度的号，启动官方客户端
  gcccode sessions              这个目录最近的会话
  gcccode app                   桌面窗口版的面板（macOS；其他系统退回浏览器）
  gcccode web                   在浏览器里打开本机面板：加号、换号、设代理都能点`
}

func longHelp() string {
	return `GCC Code 把 Claude / Codex / Gemini / Grok / Antigravity 的会员排成一个号池。
请求只由官方客户端发出。GCC Code 只决定用哪个号、什么时候换、历史怎么接上。
可选的本机网关（gcccode gateway）是另一条默认关闭的路径，不替代这条主路径。

` + shortHelp() + `

  gcccode account import [claude|codex|gemini|grok|agy]  把当前已登录的号收进号池
  gcccode account add claude [名称]       在这个号专用的浏览器里授权，用 claude setup-token 拿一年期 token
  gcccode account add claude --login      改用完整登录（Claude in Chrome、Remote Control 要这种）
  gcccode account token <id> [token|-]    换上新 token；不写就重新走一遍 setup-token
  gcccode account browser <id> [网址]     打开这个号专用的浏览器
  gcccode account login <id>              重新登录；token 号就是续期
  gcccode account rm <id>                 从号池移除（不删除聊天记录）
  gcccode account proxy <id> <url|off>    给这个号固定出口代理
  gcccode account disable|enable|unquarantine <id>
  gcccode use claude|codex|gemini|grok|agy <id>  指定下次用哪个号；codex 会立刻切换
  gcccode sessions [--all] [-n 10]        这个目录最近的会话、开在哪个号上
  gcccode resume [会话id或前缀]           在还有额度的号上续接；不写 id 就接这个目录最近的会话
  gcccode strategy smart|pace|manual
  gcccode continue on|off                 额度用完后是否自动续接
  gcccode relay set --anthropic URL --openai URL --token TOKEN
  gcccode relay enable|disable
  gcccode integrate [off]                 把额度写进 Claude Code 状态栏（原来的状态栏保留）；off 撤销
  gcccode shell-init                      打印 shell 函数，让五个官方命令走号池
                                          mcp、update、--version 这类管理命令照旧交给官方客户端
  gcccode lcu                             computer use（LCU）装好没有、哪些号能用
  gcccode lcu install                     下载、校验并安装 LCU，注册到 Claude Code 和 Codex
  gcccode lcu setup|doctor|apps|update    交给 lcu 执行，注册结果同步到号池
  gcccode gateway start|stop|status|env   可选网关：OpenAI / Anthropic / Gemini 入站，API Key 上游，请求级 failover
  gcccode gateway provider add|list|rm|enable|disable
  gcccode use-gateway on|off|status       gcccode 启动的 claude / codex / gemini 改走网关，或恢复直连
  gcccode doctor
  gcccode tray                            菜单栏
  gcccode version

没有工具能保证不被封号。官方条款限制把账号借给别人。
GCC Code 的定位是：自己的多个会员，按会话固定在一个号上，不伪装客户端，不绕过限额。`
}
