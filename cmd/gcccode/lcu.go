package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/nearmetips/DailiCode/internal/bins"
	"github.com/nearmetips/DailiCode/internal/claude"
	"github.com/nearmetips/DailiCode/internal/core"
	"github.com/nearmetips/DailiCode/internal/lcu"
)

var errNoLCU = errors.New("没装 LCU。先运行 gcccode lcu install")

const lcuHelp = `computer use（LCU，github.com/amontlabs/lcu）：
  gcccode lcu                     装在哪、注册了没有、号池里哪些号已经带上
  gcccode lcu install [参数]      下载、校验并安装 LCU，再注册到 Claude Code 和 Codex
      --version X  --proxy URL  --mirror URL  --existing-app 路径
      其余参数交给 lcu setup，例如 --chrome、--audio、--session direct
  gcccode lcu setup [参数]        在主目录运行 lcu setup，再同步到号池里的每个 Claude 号
  gcccode lcu <其他>              原样交给 lcu，例如 doctor、status、apps、update`

func cmdLCU(args []string) error {
	if len(args) == 0 {
		return lcuStatus()
	}
	switch args[0] {
	case "install":
		return lcuInstall(args[1:])
	case "setup":
		return lcuSetup(args[1:])
	case "help", "-h", "--help":
		fmt.Println(lcuHelp)
		return nil
	}
	return lcuPassthrough(args)
}

func lcuStatus() error {
	st, err := core.Load()
	if err != nil {
		return err
	}
	bin, ok := lcu.Command()
	if !ok {
		fmt.Println("没装 LCU。它把 ChatGPT 桌面版里 Codex 的 computer use 拆出来，给 Claude Code 和 Codex CLI 用：看窗口、点击、打字、截图。")
		lcuPrereqs(lcu.DefaultApp())
		fmt.Println("安装：gcccode lcu install")
		return nil
	}
	fmt.Printf("LCU %s  %s\n", or(lcu.Version(bin), "（版本未知）"), bin)
	warnLCUHome(st.Settings)
	home := core.ClaudeHome(st.Settings)
	reg := lcu.InspectClaude(claude.MainConfigJSON(st.Settings), filepath.Join(home, "settings.json"))
	mod := lcu.ModInstalled(home)
	codexOK := lcu.CodexRegistered(core.CodexHome(st.Settings))
	fmt.Println("Claude 主目录 ", mainLCUState(reg, mod))
	fmt.Println("Codex         ", map[bool]string{true: "已注册", false: "未注册"}[codexOK])
	if accounts := st.ByKind(core.KindClaude); reg.Server && len(accounts) > 0 {
		fmt.Println("号池里的 Claude 号")
		for _, a := range accounts {
			state, _ := accountLCUState(st, a)
			fmt.Printf("  %-16s %s\n", displayName(a), state)
		}
	}
	_, codexErr := exec.LookPath("codex")
	if !reg.Server || !reg.Hooks || !mod || (codexErr == nil && !codexOK) {
		fmt.Println("注册：gcccode lcu setup")
	}
	fmt.Println("检查桌面权限：gcccode lcu doctor")
	return nil
}

func mainLCUState(c lcu.Claude, mod bool) string {
	if !c.Server {
		return "未注册"
	}
	var missing []string
	if !c.Hooks {
		missing = append(missing, "生命周期 hook")
	}
	if !mod {
		missing = append(missing, "审批插件 lcu-approve")
	}
	if len(missing) > 0 {
		return "注册不完整，缺" + strings.Join(missing, "、")
	}
	return "已注册"
}

// accountLCUState describes whether a Claude account dir carries the main
// home's LCU registration; ok is false when something is still missing.
func accountLCUState(st *core.Store, a core.Account) (string, bool) {
	if claude.IsMainHome(st.Settings, a.ConfigDir) {
		return "就是主目录，直接能用", true
	}
	c := lcu.InspectClaude(claude.ConfigJSON(st.Settings, a.ConfigDir), filepath.Join(a.ConfigDir, "settings.json"))
	if !c.Server || !c.Hooks {
		return "还没带上，下次用这个号启动时会补上", false
	}
	if slices.Contains(claude.LinkReport(st.Settings, a.ConfigDir), "skills") {
		return "已带上，但 skills 没连到主目录，审批插件不会加载", false
	}
	return "已带上", true
}

// lcuPrereqs prints what LCU needs from this machine and reports whether all
// of it is there. LCU itself never installs the app or Python.
func lcuPrereqs(app string) bool {
	if _, err := lcu.Target(); err != nil {
		fmt.Println(err)
		return false
	}
	ok := true
	if _, err := os.Stat(app); err == nil {
		fmt.Println("ChatGPT 桌面版 ", app)
	} else {
		fmt.Printf("ChatGPT 桌面版  没找到 %s。先装：https://chatgpt.com/download/（不用登录）\n", app)
		ok = false
	}
	if p, found := lcu.Python(); found {
		fmt.Println("Python 3.12+  ", p)
	} else {
		fmt.Println("Python 3.12+   PATH 里没有。LCU 的安装器和启动器都要它")
		ok = false
	}
	return ok
}

func lcuInstall(args []string) error {
	var d lcu.Download
	var app string
	var rest []string
	for i := 0; i < len(args); i++ {
		name, val, hasVal := strings.Cut(args[i], "=")
		var dst *string
		switch name {
		case "--version":
			dst = &d.Version
		case "--proxy":
			dst = &d.Proxy
		case "--mirror":
			dst = &d.Mirror
		case "--existing-app":
			dst = &app
		default:
			rest = append(rest, args[i])
			continue
		}
		if !hasVal {
			if i+1 >= len(args) {
				return fmt.Errorf("%s 需要一个值", name)
			}
			i++
			val = args[i]
		}
		*dst = val
	}
	if _, err := lcu.Target(); err != nil {
		return err
	}
	if bin, ok := lcu.Command(); ok && d.Version == "" {
		fmt.Printf("已经装了 LCU %s（%s），跳过下载。升级用 gcccode lcu update。\n", or(lcu.Version(bin), "（版本未知）"), bin)
		return lcuSetup(rest)
	}
	if !lcuPrereqs(or(app, lcu.DefaultApp())) {
		return errors.New("先把上面缺的装好，再运行 gcccode lcu install")
	}
	st, err := core.Load()
	if err != nil {
		return err
	}
	parent := os.TempDir()
	if runtime.GOOS == "linux" {
		parent = filepath.Join(core.Root(), "lcu-download")
	}
	fmt.Println("下载 LCU …")
	r, err := lcu.Fetch(d, parent)
	if err != nil {
		return err
	}
	fmt.Printf("LCU %s 已下载，SHA-256 校验通过。\n", r.Version)
	if runtime.GOOS == "linux" {
		printLinuxLCUInstall(r, app)
		return nil
	}
	defer os.RemoveAll(r.Work)
	if err := lcu.InstallRuntime(r, app, lcu.Env(os.Environ(), st.Settings)); err != nil {
		return fmt.Errorf("LCU 安装器失败：%w", err)
	}
	return lcuSetup(rest)
}

// printLinuxLCUInstall leaves the root step to the user: LCU's Linux
// installer needs root for apt, and GCC Code does not escalate.
func printLinuxLCUInstall(r lcu.Release, app string) {
	name := os.Getenv("USER")
	if u, err := user.Current(); err == nil {
		name = u.Username
	}
	cmd := "sudo " + shellQuote(filepath.Join(r.Dir, "scripts", "install.sh")) + " --user " + shellQuote(name) + " --runtime-only"
	if app != "" {
		cmd += " --existing-app " + shellQuote(app)
	}
	fmt.Println("Linux 上装运行时要 root（会用 apt 装系统库），GCC Code 不替你提权。在桌面会话的终端里运行：")
	fmt.Println("  " + cmd)
	fmt.Println("装好后注册：")
	fmt.Println("  gcccode lcu setup --session direct")
}

func lcuSetup(args []string) error {
	for _, a := range args {
		if a == "-h" || a == "--help" || a == "--list-agents" {
			return lcuPassthrough(append([]string{"setup"}, args...))
		}
	}
	st, err := core.Load()
	if err != nil {
		return err
	}
	bin, ok := lcu.Command()
	if !ok {
		return errNoLCU
	}
	warnLCUHome(st.Settings)
	args = lcu.SetupArgs(args, lcuAgents(st))
	runErr := lcu.Run(bin, append([]string{"setup"}, args...), lcu.Env(os.Environ(), st.Settings))
	// A failed setup still keeps the harnesses that did register.
	syncLCU()
	if runErr != nil {
		return fmt.Errorf("lcu setup 没有全部成功（%w）。已经注册上的部分照样同步到了号池", runErr)
	}
	fmt.Println("已同步到号池。正在运行的 Claude Code / Codex 重开后才会加载 LCU。")
	return nil
}

// lcuAgents picks the harnesses to register when the user named none. LCU
// probes the codex on PATH for hook support, so the copy bundled inside the
// ChatGPT app does not count.
func lcuAgents(st *core.Store) []string {
	var out []string
	if _, err := bins.Claude(st.Settings); err == nil || len(st.ByKind(core.KindClaude)) > 0 {
		out = append(out, "claude-code")
	}
	if _, err := exec.LookPath("codex"); err == nil {
		out = append(out, "codex")
	} else if len(st.ByKind(core.KindCodex)) > 0 {
		fmt.Println("PATH 里没有独立版 codex，这次不注册 Codex。LCU 要的是 npm install -g @openai/codex 装的命令行版。")
	}
	return out
}

// syncLCU refreshes every idle Claude account dir from the main home, so what
// lcu setup wrote there reaches the pool before the next launch.
func syncLCU() {
	st, err := core.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "gcccode:", err)
		return
	}
	for _, a := range st.ByKind(core.KindClaude) {
		if claude.IsMainHome(st.Settings, a.ConfigDir) {
			continue
		}
		if st.Busy(a.ID) {
			fmt.Printf("%s 正在使用，退出后下次启动时带上。\n", displayName(a))
			continue
		}
		if err := claude.EnsureProfile(st.Settings, a.ConfigDir); err != nil {
			fmt.Fprintf(os.Stderr, "%s 同步失败：%v\n", displayName(a), err)
		}
	}
}

func lcuPassthrough(args []string) error {
	bin, ok := lcu.Command()
	if !ok {
		return errNoLCU
	}
	st, err := core.Load()
	if err != nil {
		return err
	}
	err = lcu.Run(bin, args, lcu.Env(os.Environ(), st.Settings))
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		os.Exit(exit.ExitCode())
	}
	return err
}

func warnLCUHome(s core.Settings) {
	if !lcu.DefaultHome(s) {
		fmt.Printf("注意：LCU 只注册到 ~/.claude，而号池的 Claude 主目录设成了 %s，号池里的号拿不到 LCU。\n", core.ClaudeHome(s))
	}
}

func displayName(a core.Account) string {
	if a.Label != "" {
		return a.Label
	}
	return a.ID
}

func shellQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t'\"$`\\!*?[]{}()<>|&;#~") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
