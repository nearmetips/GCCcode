// Package launcher starts the official CLI on the account the scheduler picks
// and, after that process exits, continues the same conversation on the next
// account when the quota ran out.
package launcher

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/nearmetips/DailiCode/internal/agy"
	"github.com/nearmetips/DailiCode/internal/bins"
	"github.com/nearmetips/DailiCode/internal/claude"
	"github.com/nearmetips/DailiCode/internal/codex"
	"github.com/nearmetips/DailiCode/internal/core"
	"github.com/nearmetips/DailiCode/internal/gateway"
	"github.com/nearmetips/DailiCode/internal/gemini"
	"github.com/nearmetips/DailiCode/internal/grok"
	"github.com/nearmetips/DailiCode/internal/handoff"
	"github.com/nearmetips/DailiCode/internal/quota"
	"github.com/nearmetips/DailiCode/internal/relay"
	"github.com/nearmetips/DailiCode/internal/scheduler"
)

// LaunchEnv carries a launch id into the official client. The status line
// inherits it and records which session that launch is running, so parallel
// sessions are never mistaken for one another when one of them exits.
const LaunchEnv = "GCCCODE_LAUNCH"

// Options controls one launch.
type Options struct {
	Kind    string
	Account string
	Args    []string
	Resume  string
	Relay   bool
	// Dir is where the client runs; empty means the current directory.
	Dir    string
	Stdout io.Writer
	Stderr io.Writer
}

func (o Options) out() io.Writer {
	if o.Stderr != nil {
		return o.Stderr
	}
	return os.Stderr
}

func knownKind(kind string) bool {
	for _, k := range core.Kinds {
		if k == kind {
			return true
		}
	}
	return false
}

// Run picks an account and runs the official client in the foreground.
func Run(o Options) error {
	if !knownKind(o.Kind) {
		return fmt.Errorf("未知的客户端：%s", o.Kind)
	}
	note := func(f string, args ...any) { fmt.Fprintf(o.out(), f+"\n", args...) }
	if o.Dir == "" {
		o.Dir, _ = os.Getwd()
	}
	if o.Account == "" && !o.Relay {
		st, err := core.Load()
		if err != nil {
			return err
		}
		if gateway.Uses(st.Settings, o.Kind) {
			return runGateway(st, o, note)
		}
	}

	for hop := 0; hop < 6; hop++ {
		st, err := core.Mutate(func(s *core.Store) error {
			s.PruneOccupancy()
			quota.RefreshCodex(s, time.Now())
			return nil
		})
		if err != nil {
			return err
		}

		if o.Relay {
			return runRelay(st, o, note)
		}
		acc, err := choose(st, o)
		if errors.Is(err, scheduler.ErrNone) {
			if (o.Kind == core.KindClaude || o.Kind == core.KindCodex) && relay.Ready(st.Settings.Relay) {
				note("订阅账号都不可用，改走中转 %s。", st.Settings.Relay.Name)
				return runRelay(st, o, note)
			}
			if len(st.ByKind(o.Kind)) == 0 {
				return fmt.Errorf("还没有 %s 账号。先运行 gcccode account add %s", o.Kind, o.Kind)
			}
			return fmt.Errorf("%s。可以再加一个号，或 gcccode relay set 配好中转", noneLeft(st, o.Kind, time.Now()))
		}
		if err != nil {
			return err
		}
		started := time.Now()
		launch := newLaunchID()
		exitErr := runAccount(st, acc, o, launch, note)
		next, cont, herr := after(acc, &o, launch, started, note)
		if herr != nil {
			note("换号后续接失败：%v", herr)
		}
		if exitErr != nil && !cont {
			return exitErr
		}
		if !cont {
			return nil
		}
		o.Account = next
		o.Relay = false
		note("在 %s 上继续这个会话。", next)
	}
	return errors.New("连续换号次数过多，已停止")
}

func choose(st *core.Store, o Options) (core.Account, error) {
	ready := readyFn(st)
	now := time.Now()
	if o.Account != "" {
		a, ok := st.Account(o.Account)
		if !ok || a.Kind != o.Kind {
			return core.Account{}, fmt.Errorf("没有这个 %s 账号：%s", o.Kind, o.Account)
		}
		if !ready(*a) {
			return core.Account{}, fmt.Errorf("账号 %s 还没有登录", a.ID)
		}
		return *a, nil
	}
	if a, ok := sessionOwner(st, o, ready, now); ok {
		return a, nil
	}
	picked, _, err := scheduler.Pick(st, o.Kind, ready, nil, now)
	if err != nil {
		return core.Account{}, err
	}
	return picked.Account, nil
}

// sessionOwner is the account that started the session being resumed,
// when it can take a session now. Only that account resumes the cloud
// session; any other gets a handoff, so it goes first.
func sessionOwner(st *core.Store, o Options, ready scheduler.Ready, now time.Time) (core.Account, bool) {
	if o.Resume == "" || st.Settings.Strategy == "manual" {
		return core.Account{}, false
	}
	if o.Kind != core.KindClaude && o.Kind != core.KindGemini && o.Kind != core.KindGrok {
		return core.Account{}, false
	}
	owner := st.State.SessionOwner[o.Resume]
	if owner == "" {
		return core.Account{}, false
	}
	for _, s := range scheduler.Evaluate(st, o.Kind, ready, now) {
		if s.Account.ID == owner && s.Usable() {
			return s.Account, true
		}
	}
	return core.Account{}, false
}

func readyFn(st *core.Store) scheduler.Ready {
	return func(a core.Account) bool {
		switch a.Kind {
		case core.KindClaude:
			return claude.Ready(st.Settings, a)
		case core.KindCodex:
			_, err := os.Stat(codex.VaultPath(a.ID))
			return err == nil
		case core.KindGemini:
			return gemini.Ready(a.ConfigDir)
		case core.KindGrok:
			return grok.Ready(a.ConfigDir)
		case core.KindAgy:
			return agy.Ready(a.ConfigDir)
		}
		return false
	}
}

func runAccount(st *core.Store, acc core.Account, o Options, launch string, note func(string, ...any)) error {
	switch acc.Kind {
	case core.KindClaude:
		if err := claude.EnsureProfile(st.Settings, acc.ConfigDir); err != nil {
			return err
		}
		bin, err := bins.Claude(st.Settings)
		if err != nil {
			return fmt.Errorf("找不到 claude 命令：%w", err)
		}
		if msg := claude.EgressProblem(st.Settings, st.Accounts, acc.ID); msg != "" {
			return errors.New(msg)
		}
		if cleared, err := claude.SeparateDevices(st.Settings, st.Accounts); err != nil {
			return err
		} else if len(cleared) > 0 {
			note("这些号的设备标识和另一个 Claude 会员重复，已清掉，官方客户端会各自重新生成：%s", strings.Join(cleared, ", "))
		}
		args, err := claudeArgs(st, acc, o, note)
		if err != nil {
			return err
		}
		env := setEnv(claudeEnv(os.Environ(), st.Settings, acc), LaunchEnv, launch)
		note("Claude 使用账号 %s。", labelWithEmail(acc))
		return foreground(bin, args, env, acc.ID, o.Dir)
	case core.KindCodex:
		bin, err := bins.Codex(st.Settings)
		if err != nil {
			return fmt.Errorf("找不到 codex 命令：%w", err)
		}
		var activated string
		_, err = core.Mutate(func(s *core.Store) error {
			if s.State.CodexCurrent == acc.ID && sameFile(codex.LivePath(s.Settings), codex.VaultPath(acc.ID)) {
				return nil
			}
			if err := codex.Activate(s, acc.ID, false); err != nil {
				return err
			}
			activated = acc.ID
			return nil
		})
		if err != nil {
			return err
		}
		if activated != "" {
			note("Codex 已切到 %s。正在运行的 Codex 桌面版需要重新打开才会用这个号。", label(acc))
		}
		args := o.Args
		if o.Resume != "" && !has(args, "resume") {
			args = append([]string{"resume", o.Resume}, args...)
		}
		env := setEnv(codexEnv(os.Environ(), st.Settings, acc), LaunchEnv, launch)
		note("Codex 使用账号 %s。", labelWithEmail(acc))
		return foreground(bin, args, env, acc.ID, o.Dir)
	case core.KindGemini:
		if err := gemini.EnsureProfile(st.Settings, acc.ConfigDir); err != nil {
			return err
		}
		if msg := gemini.ProxyProblem(acc.Proxy); msg != "" {
			return errors.New(msg)
		}
		bin, err := bins.Gemini(st.Settings)
		if err != nil {
			return fmt.Errorf("找不到 gemini 命令：%w", err)
		}
		args, err := isolatedResumeArgs(st, acc, o, note, geminiHandoff)
		if err != nil {
			return err
		}
		env := setEnv(geminiEnv(os.Environ(), st.Settings, acc), LaunchEnv, launch)
		note("Gemini 使用账号 %s。", labelWithEmail(acc))
		return foreground(bin, args, env, acc.ID, o.Dir)
	case core.KindGrok:
		if err := grok.EnsureProfile(st.Settings, acc.ConfigDir); err != nil {
			return err
		}
		if msg := grok.ProxyProblem(acc.Proxy); msg != "" {
			return errors.New(msg)
		}
		bin, err := bins.Grok(st.Settings)
		if err != nil {
			return fmt.Errorf("找不到 grok 命令：%w", err)
		}
		args, err := isolatedResumeArgs(st, acc, o, note, grokHandoff)
		if err != nil {
			return err
		}
		env := setEnv(grokEnv(os.Environ(), st.Settings, acc), LaunchEnv, launch)
		note("Grok 使用账号 %s。", labelWithEmail(acc))
		return foreground(bin, args, env, acc.ID, o.Dir)
	case core.KindAgy:
		if msg := agy.ProxyProblem(acc.Proxy); msg != "" {
			return errors.New(msg)
		}
		bin, err := bins.Agy(st.Settings)
		if err != nil {
			return fmt.Errorf("找不到 agy 命令：%w", err)
		}
		args := withResume(o.Args, o.Resume, "--conversation")
		env := setEnv(agyEnv(os.Environ(), st.Settings, acc), LaunchEnv, launch)
		note("Antigravity 使用账号 %s。", labelWithEmail(acc))
		return foreground(bin, args, env, acc.ID, o.Dir)
	}
	return nil
}

func geminiHandoff(st *core.Store, acc core.Account, id string) (string, error) {
	sess, ok := gemini.FindSession(st.Settings, acc.ConfigDir, id)
	if !ok {
		sess, ok = gemini.FindSession(st.Settings, "", id)
	}
	if !ok {
		return "", fmt.Errorf("找不到会话 %s", id)
	}
	return handoff.WriteGemini(sess.Path, id)
}

func grokHandoff(st *core.Store, acc core.Account, id string) (string, error) {
	sess, ok := grok.FindSession(st.Settings, acc.ConfigDir, id)
	if !ok {
		sess, ok = grok.FindSession(st.Settings, "", id)
	}
	if !ok {
		return "", fmt.Errorf("找不到会话 %s", id)
	}
	return handoff.WriteGrok(sess.Path, id)
}

// isolatedResumeArgs resumes only on the account that owns the session.
// Another account of the same kind gets a local handoff (Gemini/Grok homes
// do not share cloud sessions across profiles).
func isolatedResumeArgs(st *core.Store, acc core.Account, o Options, note func(string, ...any), write func(*core.Store, core.Account, string) (string, error)) ([]string, error) {
	if o.Resume == "" || has(o.Args, "--resume") || has(o.Args, "-r") {
		return o.Args, nil
	}
	owner := st.State.SessionOwner[o.Resume]
	if owner == acc.ID || (owner == "" && len(st.ByKind(acc.Kind)) < 2) {
		return withResume(o.Args, o.Resume, "--resume"), nil
	}
	path, err := write(st, acc, o.Resume)
	if err != nil {
		return nil, err
	}
	note("不把云端会话接到另一个 %s 账号上。本地记录在 %s", acc.Kind, path)
	return append(append([]string{}, o.Args...), handoff.Prompt+path), nil
}

func after(acc core.Account, o *Options, launch string, started time.Time, note func(string, ...any)) (next string, cont bool, err error) {
	st, err := core.Load()
	if err != nil {
		return "", false, err
	}
	var sessionID, transcript string
	var sigKind string
	_, err = core.Mutate(func(s *core.Store) error {
		switch acc.Kind {
		case core.KindClaude:
			if sess, ok := claudeSession(s, acc.ID, launch, o.Dir, started); ok {
				sessionID, transcript = sess.ID, sess.Path
				s.State.SessionOwner[sess.ID] = acc.ID
				quota.ApplySignal(s, acc.ID, claude.ScanTranscript(sess.Path, started))
			}
		case core.KindCodex:
			if _, _, err := codex.SyncBack(s); err != nil {
				note("写回 Codex token 失败：%v", err)
			}
			quota.RefreshCodex(s, time.Now())
			if sess, ok := codex.LatestSessionFor(s.Settings, started.Add(-2*time.Second), o.Dir, o.Resume); ok {
				sessionID, transcript = sess.ID, sess.Path
				quota.ApplySignal(s, acc.ID, codex.ScanSignals(sess.Path, started))
				if o.Resume != "" && codex.EncryptedReplayFailed(sess.Path, started) {
					sigKind = "encrypted"
				}
			}
		case core.KindGemini:
			if sess, ok := gemini.Latest(s.Settings, acc.ConfigDir, o.Dir, started.Add(-2*time.Second)); ok {
				sessionID, transcript = sess.ID, sess.Path
				s.State.SessionOwner[sess.ID] = acc.ID
				quota.ApplySignal(s, acc.ID, gemini.ScanSignals(sess.Path, started))
			}
		case core.KindGrok:
			if sess, ok := grok.Latest(s.Settings, acc.ConfigDir, o.Dir, started.Add(-2*time.Second)); ok {
				sessionID, transcript = sess.ID, sess.Path
				s.State.SessionOwner[sess.ID] = acc.ID
				quota.ApplySignal(s, acc.ID, grok.ScanSignals(sess.Path, started))
			}
		case core.KindAgy:
			if o.Resume != "" {
				sessionID = o.Resume
			} else if sess, ok := agy.Latest(s.Settings, acc.ConfigDir, o.Dir, started.Add(-2*time.Second)); ok {
				sessionID = sess.ID
			}
			if sessionID != "" {
				s.State.SessionOwner[sessionID] = acc.ID
			}
			quota.ApplySignal(s, acc.ID, agy.ScanSignals(acc.ConfigDir, started))
		}
		return nil
	})
	if err != nil {
		return "", false, err
	}
	if sigKind == "encrypted" && transcript != "" {
		path, herr := handoff.WriteCodex(transcript, sessionID)
		if herr != nil {
			return "", false, herr
		}
		note("新账号读不了上一个号的加密推理记录。已写好接力文件：%s", path)
		o.Resume = ""
		o.Args = append(o.Args, handoff.Prompt+path)
	} else if acc.Kind == core.KindClaude || acc.Kind == core.KindGemini || acc.Kind == core.KindGrok || acc.Kind == core.KindAgy {
		o.Resume = ""
	} else if sessionID != "" {
		o.Resume = sessionID
	}
	st, err = core.Load()
	if err != nil {
		return "", false, err
	}
	exhausted, _ := st.Exhausted(acc.ID, time.Now())
	a, _ := st.Account(acc.ID)
	quarantined := a != nil && a.Quarantined
	if !exhausted && !quarantined && sigKind != "encrypted" {
		return "", false, nil
	}
	ready := readyFn(st)
	picked, _, perr := scheduler.Pick(st, acc.Kind, ready, []string{acc.ID}, time.Now())
	if perr != nil {
		if !errors.Is(perr, scheduler.ErrNone) {
			return "", false, nil
		}
		if relay.Ready(st.Settings.Relay) {
			note("其余订阅账号也不可用。可以运行 gcccode %s --relay 改走中转。", acc.Kind)
		} else {
			note("%s。", noneLeft(st, acc.Kind, time.Now()))
		}
		if sessionID != "" {
			note("会话还在，恢复后接着做：gcccode resume %s", sessionID)
		}
		return "", false, nil
	}
	switch acc.Kind {
	case core.KindClaude:
		if transcript == "" {
			return "", false, nil
		}
		path, herr := handoff.WriteClaude(transcript, sessionID)
		if herr != nil {
			return "", false, herr
		}
		o.Args = append(o.Args, handoff.Prompt+path)
		note("不把云端会话接到另一个 Claude 会员上。本地记录在 %s", path)
	case core.KindGemini:
		if transcript == "" {
			return "", false, nil
		}
		path, herr := handoff.WriteGemini(transcript, sessionID)
		if herr != nil {
			return "", false, herr
		}
		o.Args = append(o.Args, handoff.Prompt+path)
		note("不把云端会话接到另一个 Gemini 账号上。本地记录在 %s", path)
	case core.KindGrok:
		if transcript == "" {
			return "", false, nil
		}
		path, herr := handoff.WriteGrok(transcript, sessionID)
		if herr != nil {
			return "", false, herr
		}
		o.Args = append(o.Args, handoff.Prompt+path)
		note("不把云端会话接到另一个 Grok 账号上。本地记录在 %s", path)
	default:
		if o.Resume == "" && sigKind != "encrypted" {
			return "", false, nil
		}
	}
	if !st.Settings.AutoContinue && !confirm("账号 "+label(acc)+" 已不可用，用 "+label(picked.Account)+" 续接这个会话？") {
		if o.Resume != "" {
			note("会话还在。手动续接：gcccode %s --account %s --resume %s", acc.Kind, picked.Account.ID, o.Resume)
		}
		return "", false, nil
	}
	o.Account = picked.Account.ID
	return picked.Account.ID, true, nil
}

// claudeSession is the transcript one launch wrote. The status line reports
// it from inside the launch; without the status line, the newest transcript
// started in dir that no other account owns is the best guess.
func claudeSession(s *core.Store, accountID, launch, dir string, started time.Time) (claude.Session, bool) {
	l, ok := s.State.Launches[launch]
	delete(s.State.Launches, launch)
	for id, old := range s.State.Launches {
		if time.Since(old.At) > 7*24*time.Hour {
			delete(s.State.Launches, id)
		}
	}
	if ok && l.SessionID != "" {
		if l.Transcript != "" {
			if fi, err := os.Stat(l.Transcript); err == nil {
				return claude.Session{ID: l.SessionID, Path: l.Transcript, Mod: fi.ModTime()}, true
			}
		}
		if sess, ok := claude.FindSession(s.Settings, l.SessionID); ok {
			return sess, true
		}
	}
	return claude.Latest(s.Settings, dir, started.Add(-2*time.Second), func(id string) bool {
		owner := s.State.SessionOwner[id]
		return owner != "" && owner != accountID
	})
}

func runRelay(st *core.Store, o Options, note func(string, ...any)) error {
	switch o.Kind {
	case core.KindClaude:
		dir, err := relay.PrepareClaude(st.Settings)
		if err != nil {
			return err
		}
		bin, err := bins.Claude(st.Settings)
		if err != nil {
			return err
		}
		args := withResume(o.Args, o.Resume, "--resume")
		return foreground(bin, args, relay.ClaudeEnv(setEnv(os.Environ(), LaunchEnv, ""), dir), "", o.Dir)
	case core.KindCodex:
		restore, err := relay.ActivateCodex(st.Settings)
		if err != nil {
			return err
		}
		defer restore()
		bin, err := bins.Codex(st.Settings)
		if err != nil {
			return err
		}
		args := o.Args
		if o.Resume != "" && !has(args, "resume") {
			args = append([]string{"resume", o.Resume}, args...)
		}
		note("Codex 本次走中转。结束后会把 config.toml 改回去。")
		return foreground(bin, args, relay.CodexEnv(setEnv(os.Environ(), LaunchEnv, ""), st.Settings.Relay), "", o.Dir)
	}
	return fmt.Errorf("%s 还不支持中转兜底，请再加一个号或等额度恢复", o.Kind)
}

var management = map[string]map[string]bool{
	core.KindClaude: words("agents attach auth auto-mode config doctor gateway import install logs mcp " +
		"migrate-installer plugin plugins purge respawn rm setup-token stop kill update upgrade -v --version -h --help"),
	core.KindCodex: words("login logout mcp mcp-server plugin completion update doctor sandbox debug apply a " +
		"agents queue archive delete unarchive migrate-rollouts app app-server remote-control exec-server " +
		"features help -V --version -h --help"),
	core.KindGemini: words("auth login logout mcp extensions skills settings stats about docs update " +
		"-v --version -h --help"),
	core.KindGrok: words("login logout sessions mcp plugin plugins skills doctor update completion " +
		"-V --version -h --help"),
	core.KindAgy: words("agent agents changelog help install mcp mic-serve models plugin plugins remote-control update " +
		"-v --version -h --help"),
}

func words(s string) map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(s) {
		m[w] = true
	}
	return m
}

// Management reports whether args run an official subcommand that starts no
// conversation (mcp, update, --version...). Those must not pick an account.
func Management(kind string, args []string) bool {
	return len(args) > 0 && management[kind][args[0]]
}

// Passthrough runs a management subcommand exactly as the official client
// would without gcccode. With account set, a Claude subcommand runs inside
// that account's directory and egress instead.
func Passthrough(kind, account string, args []string) error {
	st, err := core.Load()
	if err != nil {
		return err
	}
	env := setEnv(os.Environ(), LaunchEnv, "")
	var bin string
	switch kind {
	case core.KindClaude:
		if bin, err = bins.Claude(st.Settings); err != nil {
			return fmt.Errorf("找不到 claude 命令：%w", err)
		}
		if account != "" {
			a, ok := st.Account(account)
			if !ok || a.Kind != kind {
				return fmt.Errorf("没有这个 claude 账号：%s", account)
			}
			if err := claude.EnsureProfile(st.Settings, a.ConfigDir); err != nil {
				return err
			}
			env = claudeEnv(env, st.Settings, *a)
		}
	case core.KindCodex:
		if account != "" {
			return fmt.Errorf("Codex 的管理命令总是用当前登录的号。要先换号：gcccode use codex %s", account)
		}
		if bin, err = bins.Codex(st.Settings); err != nil {
			return fmt.Errorf("找不到 codex 命令：%w", err)
		}
		if args[0] == "logout" {
			if _, err := core.Mutate(func(s *core.Store) error {
				_, _, err := codex.SyncBack(s)
				return err
			}); err != nil {
				return fmt.Errorf("退出登录前没能把当前 token 写回号池，已中止：%w", err)
			}
		}
	case core.KindGemini:
		if bin, err = bins.Gemini(st.Settings); err != nil {
			return fmt.Errorf("找不到 gemini 命令：%w", err)
		}
		if account != "" {
			a, ok := st.Account(account)
			if !ok || a.Kind != kind {
				return fmt.Errorf("没有这个 gemini 账号：%s", account)
			}
			if err := gemini.EnsureProfile(st.Settings, a.ConfigDir); err != nil {
				return err
			}
			env = geminiEnv(env, st.Settings, *a)
		}
	case core.KindGrok:
		if bin, err = bins.Grok(st.Settings); err != nil {
			return fmt.Errorf("找不到 grok 命令：%w", err)
		}
		if account != "" {
			a, ok := st.Account(account)
			if !ok || a.Kind != kind {
				return fmt.Errorf("没有这个 grok 账号：%s", account)
			}
			if err := grok.EnsureProfile(st.Settings, a.ConfigDir); err != nil {
				return err
			}
			env = grokEnv(env, st.Settings, *a)
		}
	case core.KindAgy:
		if bin, err = bins.Agy(st.Settings); err != nil {
			return fmt.Errorf("找不到 agy 命令：%w", err)
		}
		if account != "" {
			a, ok := st.Account(account)
			if !ok || a.Kind != kind {
				return fmt.Errorf("没有这个 agy 账号：%s", account)
			}
			env = agyEnv(env, st.Settings, *a)
		}
	default:
		return fmt.Errorf("未知的客户端：%s", kind)
	}
	cmd := exec.Command(bin, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = env
	return cmd.Run()
}

func newLaunchID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// setEnv replaces key in env; an empty value removes it.
func setEnv(env []string, key, value string) []string {
	out := make([]string, 0, len(env)+1)
	for _, e := range env {
		if k, _, _ := strings.Cut(e, "="); k != key {
			out = append(out, e)
		}
	}
	if value != "" {
		out = append(out, key+"="+value)
	}
	return out
}

func foreground(bin string, args, env []string, accountID, dir string) error {
	cmd := exec.Command(bin, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = env
	cmd.Dir = dir
	if err := cmd.Start(); err != nil {
		return err
	}
	if accountID != "" && cmd.Process != nil {
		_, _ = core.Mutate(func(s *core.Store) error {
			s.State.Occupied[accountID] = core.Occupancy{PID: cmd.Process.Pid, Since: time.Now()}
			return nil
		})
	}
	waitErr := cmd.Wait()
	if accountID != "" && cmd.Process != nil {
		_, _ = core.Mutate(func(s *core.Store) error {
			if cur, ok := s.State.Occupied[accountID]; ok && cur.PID == cmd.Process.Pid {
				delete(s.State.Occupied, accountID)
			}
			return nil
		})
	}
	return waitErr
}

// ChildEnv is the environment a subscription session should run with.
func ChildEnv(s core.Settings, acc core.Account) []string {
	switch acc.Kind {
	case core.KindClaude:
		return claudeEnv(os.Environ(), s, acc)
	case core.KindGemini:
		return geminiEnv(os.Environ(), s, acc)
	case core.KindGrok:
		return grokEnv(os.Environ(), s, acc)
	case core.KindCodex:
		return codexEnv(os.Environ(), s, acc)
	case core.KindAgy:
		return agyEnv(os.Environ(), s, acc)
	default:
		return proxyEnv(os.Environ(), acc.Proxy)
	}
}

// codexEnv keeps a pooled Codex on its ChatGPT login: an OPENAI_BASE_URL left
// over from `gateway env` would send it to the gateway's upstreams instead.
// Other OPENAI_BASE_URL values are the user's own choice and stay.
func codexEnv(base []string, s core.Settings, acc core.Account) []string {
	gw := false
	for _, e := range base {
		if k, v, _ := strings.Cut(e, "="); k == "OPENAI_BASE_URL" {
			gw = gateway.PointsAt(s, v)
		}
	}
	var out []string
	for _, e := range base {
		k, _, _ := strings.Cut(e, "=")
		if !gw || (k != "OPENAI_BASE_URL" && k != "OPENAI_API_KEY") {
			out = append(out, e)
		}
	}
	return proxyEnv(out, acc.Proxy)
}

func claudeEnv(base []string, s core.Settings, acc core.Account) []string {
	drop := map[string]bool{
		"CLAUDE_CONFIG_DIR": true, "ANTHROPIC_BASE_URL": true, "ANTHROPIC_AUTH_TOKEN": true,
		"ANTHROPIC_API_KEY": true, "CLAUDE_CODE_OAUTH_TOKEN": true,
	}
	var out []string
	for _, e := range base {
		k, _, _ := strings.Cut(e, "=")
		if !drop[k] {
			out = append(out, e)
		}
	}
	if !claude.IsMainHome(s, acc.ConfigDir) {
		out = append(out, "CLAUDE_CONFIG_DIR="+acc.ConfigDir)
	}
	if claude.UsesToken(acc) {
		if tok, ok := claude.ReadToken(acc.ID); ok {
			out = append(out, claude.TokenEnv+"="+tok)
		}
	}
	return proxyEnv(out, claude.EffectiveProxy(s, acc))
}

func geminiEnv(base []string, s core.Settings, acc core.Account) []string {
	drop := map[string]bool{
		"GEMINI_CONFIG_DIR": true, "GEMINI_CLI_HOME": true,
		"GOOGLE_GEMINI_BASE_URL": true, "GEMINI_API_KEY": true, "GOOGLE_API_KEY": true,
	}
	var out []string
	for _, e := range base {
		k, _, _ := strings.Cut(e, "=")
		if !drop[k] {
			out = append(out, e)
		}
	}
	if !gemini.IsMainHome(s, acc.ConfigDir) {
		out = append(out, "GEMINI_CONFIG_DIR="+acc.ConfigDir)
	}
	return proxyEnv(out, acc.Proxy)
}

func grokEnv(base []string, s core.Settings, acc core.Account) []string {
	drop := map[string]bool{"GROK_HOME": true, "XAI_API_KEY": true}
	var out []string
	for _, e := range base {
		k, _, _ := strings.Cut(e, "=")
		if !drop[k] {
			out = append(out, e)
		}
	}
	if !grok.IsMainHome(s, acc.ConfigDir) {
		out = append(out, "GROK_HOME="+acc.ConfigDir)
	}
	return proxyEnv(out, acc.Proxy)
}

// agyEnv runs agy in the account's HOME. Gemini API-key and ADC variables
// would make agy skip the account's Google login, so they are dropped.
// Only a HOME that reaches the user's keychain sees agy's login.
func agyEnv(base []string, s core.Settings, acc core.Account) []string {
	drop := map[string]bool{
		"HOME": true, "USERPROFILE": true, "AGY_ADC_AUTH": true,
		"GOOGLE_GEMINI_BASE_URL": true, "GEMINI_API_KEY": true, "GOOGLE_API_KEY": true,
	}
	var out []string
	for _, e := range base {
		k, _, _ := strings.Cut(e, "=")
		if !drop[k] {
			out = append(out, e)
		}
	}
	home := acc.ConfigDir
	if home == "" {
		home = core.AgyHome(s)
	}
	out = append(out, "HOME="+home)
	if runtime.GOOS == "windows" {
		out = append(out, "USERPROFILE="+home)
	}
	return proxyEnv(out, acc.Proxy)
}

func proxyEnv(base []string, proxy string) []string {
	if proxy == "" {
		return base
	}
	drop := map[string]bool{
		"HTTP_PROXY": true, "HTTPS_PROXY": true, "ALL_PROXY": true,
		"http_proxy": true, "https_proxy": true, "all_proxy": true,
	}
	var out []string
	for _, e := range base {
		k, _, _ := strings.Cut(e, "=")
		if !drop[k] {
			out = append(out, e)
		}
	}
	for _, k := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
		out = append(out, k+"="+proxy)
	}
	return out
}

// claudeArgs resumes a session only on the account that started it. Another
// Claude subscription gets a local handoff, not the cloud session.
func claudeArgs(st *core.Store, acc core.Account, o Options, note func(string, ...any)) ([]string, error) {
	if o.Resume == "" || has(o.Args, "--resume") {
		return o.Args, nil
	}
	owner := st.State.SessionOwner[o.Resume]
	if owner == acc.ID || (owner == "" && len(st.ByKind(core.KindClaude)) < 2) {
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
	note("不把云端会话接到另一个 Claude 会员上，避免两个号被关联。本地记录在 %s", path)
	return append(append([]string{}, o.Args...), handoff.Prompt+path), nil
}

func withResume(args []string, id, flag string) []string {
	if id == "" || has(args, flag) || has(args, "resume") {
		return args
	}
	return append([]string{flag, id}, args...)
}

func has(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

// noneLeft says that no account of kind can take a session, and when the
// first one comes back.
func noneLeft(st *core.Store, kind string, now time.Time) string {
	msg := "没有可用的 " + kind + " 账号"
	if w := scheduler.Waiting(scheduler.Evaluate(st, kind, readyFn(st), now), now); w != "" {
		msg += "。" + w
	}
	return msg
}

func label(a core.Account) string {
	if a.Label != "" {
		return a.Label
	}
	return a.ID
}

func labelWithEmail(a core.Account) string {
	if a.Email == "" || a.Email == label(a) {
		return label(a)
	}
	return label(a) + "（" + a.Email + "）"
}

func sameFile(a, b string) bool {
	ab, err1 := os.ReadFile(a)
	bb, err2 := os.ReadFile(b)
	return err1 == nil && err2 == nil && string(ab) == string(bb)
}

func confirm(prompt string) bool {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return false
	}
	defer tty.Close()
	fmt.Fprintf(tty, "%s [y/N] ", prompt)
	buf := make([]byte, 16)
	n, _ := tty.Read(buf)
	a := strings.TrimSpace(string(buf[:n]))
	return a == "y" || a == "Y" || strings.EqualFold(a, "yes")
}
