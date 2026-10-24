package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/nearmetips/DailiCode/internal/agy"
	"github.com/nearmetips/DailiCode/internal/bins"
	"github.com/nearmetips/DailiCode/internal/claude"
	"github.com/nearmetips/DailiCode/internal/codex"
	"github.com/nearmetips/DailiCode/internal/core"
	"github.com/nearmetips/DailiCode/internal/gemini"
	"github.com/nearmetips/DailiCode/internal/grok"
	"github.com/nearmetips/DailiCode/internal/launcher"
	"github.com/nearmetips/DailiCode/internal/quota"
	"github.com/nearmetips/DailiCode/internal/scheduler"
)

func cmdAccounts() error {
	st, err := core.Mutate(func(s *core.Store) error {
		s.PruneOccupancy()
		quota.RefreshCodex(s, time.Now())
		return nil
	})
	if err != nil {
		return err
	}
	now := time.Now()
	ready := accountReady(st)
	any := false
	for _, kind := range core.Kinds {
		rows := scheduler.Evaluate(st, kind, ready, now)
		if len(rows) == 0 {
			continue
		}
		any = true
		fmt.Printf("%s\n", kind)
		for _, row := range rows {
			name := row.Account.Label
			if name == "" {
				name = row.Account.ID
			}
			extra := ""
			if row.Account.Email != "" && row.Account.Email != name {
				extra = "  " + row.Account.Email
			}
			if row.Account.Plan != "" {
				extra += "  " + row.Account.Plan
			}
			usage := "额度未知"
			if row.HasQuota {
				usage = scheduler.FormatLine("", row.Quota, true, "", now)
				usage = strings.TrimPrefix(usage, " · ")
			}
			fmt.Printf("  %-16s %s%s\n             %s\n", name, row.Reason, extra, usage)
		}
		if line := scheduler.NoneUsable(rows, now); line != "" {
			fmt.Println("  " + line)
		}
	}
	if !any {
		fmt.Println("号池是空的。gcccode account add claude 或 gcccode account add codex")
	}
	return nil
}

func cmdAccount(args []string) error {
	if len(args) == 0 {
		return cmdAccounts()
	}
	switch args[0] {
	case "add":
		if len(args) < 2 {
			return fmt.Errorf("用法：gcccode account add claude|codex|gemini|grok|agy [名称]")
		}
		label := ""
		if len(args) > 2 {
			label = strings.Join(args[2:], " ")
		}
		switch args[1] {
		case core.KindClaude:
			return addClaude(label)
		case core.KindCodex:
			return addCodex(label)
		case core.KindGemini:
			return addGemini(label)
		case core.KindGrok:
			return addGrok(label)
		case core.KindAgy:
			return addAgy(label)
		default:
			return fmt.Errorf("只能添加 claude、codex、gemini、grok 或 agy")
		}
	case "import":
		kind := ""
		if len(args) > 1 {
			kind = args[1]
		}
		return importCurrent(kind)
	case "login":
		if len(args) < 2 {
			return fmt.Errorf("用法：gcccode account login <id>")
		}
		return loginAccount(args[1])
	case "rm", "remove":
		if len(args) < 2 {
			return fmt.Errorf("用法：gcccode account rm <id>")
		}
		return removeAccount(args[1])
	case "proxy":
		if len(args) < 3 {
			return fmt.Errorf("用法：gcccode account proxy <id> <url|off>")
		}
		return setProxy(args[1], args[2])
	case "disable", "enable", "unquarantine":
		if len(args) < 2 {
			return fmt.Errorf("用法：gcccode account %s <id>", args[0])
		}
		return setFlag(args[0], args[1])
	default:
		return fmt.Errorf("未知的 account 子命令 %q", args[0])
	}
}

func addClaude(label string) error {
	var acc core.Account
	_, err := core.Mutate(func(s *core.Store) error {
		id := s.NewID(core.KindClaude, label)
		if label == "" {
			label = id
		}
		acc = core.Account{ID: id, Kind: core.KindClaude, Label: label, ConfigDir: core.ClaudeProfileDir(id), CreatedAt: time.Now()}
		if err := claude.EnsureProfile(s.Settings, acc.ConfigDir); err != nil {
			return err
		}
		s.Accounts = append(s.Accounts, acc)
		return nil
	})
	if err != nil {
		return err
	}
	fmt.Printf("已建立 Claude 账号目录 %s\n请在接下来的窗口里登录这个会员。\n", acc.ID)
	if err := runClaudeLogin(acc); err != nil {
		return err
	}
	return refreshClaudeIdentity(acc.ID)
}

func addCodex(label string) error {
	fmt.Println("请在接下来的窗口里登录这个 ChatGPT 会员。当前号会先写回号池。")
	return codexLogin("", label)
}

func addGemini(label string) error {
	var acc core.Account
	_, err := core.Mutate(func(s *core.Store) error {
		id := s.NewID(core.KindGemini, label)
		if label == "" {
			label = id
		}
		acc = core.Account{ID: id, Kind: core.KindGemini, Label: label, ConfigDir: core.GeminiProfileDir(id), CreatedAt: time.Now()}
		if err := gemini.EnsureProfile(s.Settings, acc.ConfigDir); err != nil {
			return err
		}
		s.Accounts = append(s.Accounts, acc)
		return nil
	})
	if err != nil {
		return err
	}
	fmt.Printf("已建立 Gemini 账号目录 %s\n请在接下来的窗口里登录这个会员。\n", acc.ID)
	if err := runGeminiLogin(acc); err != nil {
		return err
	}
	return refreshGeminiIdentity(acc.ID)
}

func addGrok(label string) error {
	var acc core.Account
	_, err := core.Mutate(func(s *core.Store) error {
		id := s.NewID(core.KindGrok, label)
		if label == "" {
			label = id
		}
		acc = core.Account{ID: id, Kind: core.KindGrok, Label: label, ConfigDir: core.GrokProfileDir(id), CreatedAt: time.Now()}
		if err := grok.EnsureProfile(s.Settings, acc.ConfigDir); err != nil {
			return err
		}
		s.Accounts = append(s.Accounts, acc)
		return nil
	})
	if err != nil {
		return err
	}
	fmt.Printf("已建立 Grok 账号目录 %s\n请在接下来的窗口里登录这个会员。\n", acc.ID)
	if err := runGrokLogin(acc); err != nil {
		return err
	}
	return refreshGrokIdentity(acc.ID)
}

// addAgy brings in the agy login on this machine, signing in first when there
// is none. agy keeps its login in one keychain entry that every HOME shares,
// so a second account would replace the first.
func addAgy(label string) error {
	st, err := core.Load()
	if err != nil {
		return err
	}
	if accs := st.ByKind(core.KindAgy); len(accs) > 0 {
		a := accs[0]
		return fmt.Errorf("号池里已经有 Antigravity 账号 %s。agy 的登录存在系统钥匙串里一个固定条目，所有目录共用，暂时只能放这一个号。要换 Google 账号：gcccode account login %s", a.ID, a.ID)
	}
	home := core.AgyHome(st.Settings)
	if !agy.Ready(home) {
		if err := runAgyLogin(core.Account{Kind: core.KindAgy, ConfigDir: home}); err != nil {
			return err
		}
	}
	return importAgy(label)
}

func importCurrent(kind string) error {
	if kind == "" || kind == core.KindClaude {
		if err := importClaude(); err != nil && kind == core.KindClaude {
			return err
		} else if err != nil {
			fmt.Fprintln(os.Stderr, "claude:", err)
		}
	}
	if kind == "" || kind == core.KindCodex {
		if err := importCodex(); err != nil && kind == core.KindCodex {
			return err
		} else if err != nil {
			fmt.Fprintln(os.Stderr, "codex:", err)
		}
	}
	if kind == "" || kind == core.KindGemini {
		if err := importGemini(); err != nil && kind == core.KindGemini {
			return err
		} else if err != nil {
			fmt.Fprintln(os.Stderr, "gemini:", err)
		}
	}
	if kind == "" || kind == core.KindGrok {
		if err := importGrok(); err != nil && kind == core.KindGrok {
			return err
		} else if err != nil {
			fmt.Fprintln(os.Stderr, "grok:", err)
		}
	}
	if kind == "" || kind == core.KindAgy {
		if err := importAgy(""); err != nil && kind == core.KindAgy {
			return err
		} else if err != nil {
			fmt.Fprintln(os.Stderr, "agy:", err)
		}
	}
	return nil
}

func importClaude() error {
	_, err := core.Mutate(func(s *core.Store) error {
		if claude.RelayInSettings(s.Settings) {
			return fmt.Errorf("~/.claude/settings.json 指向中转或 API Key，不是会员登录。用 gcccode account add claude 单独登录")
		}
		home := core.ClaudeHome(s.Settings)
		idty, ok := claude.ReadIdentity(s.Settings, home)
		if !ok {
			return fmt.Errorf("当前 Claude 没有会员登录")
		}
		for i := range s.Accounts {
			a := &s.Accounts[i]
			if a.Kind == core.KindClaude && a.RemoteID == idty.AccountUUID {
				a.Email = idty.Email
				a.ConfigDir = home
				fmt.Printf("已更新 Claude 账号 %s（%s）\n", a.ID, a.Email)
				return nil
			}
		}
		label := idty.Email
		id := s.NewID(core.KindClaude, label)
		s.Accounts = append(s.Accounts, core.Account{
			ID: id, Kind: core.KindClaude, Label: label, Email: idty.Email,
			RemoteID: idty.AccountUUID, ConfigDir: home, CreatedAt: time.Now(),
		})
		fmt.Printf("已收进 Claude 账号 %s（%s）\n", id, idty.Email)
		return nil
	})
	return err
}

func importCodex() error {
	_, err := core.Mutate(func(s *core.Store) error {
		acc, isNew, err := codex.SyncBack(s)
		if err != nil {
			return err
		}
		if acc == nil {
			return fmt.Errorf("当前没有 ChatGPT 登录")
		}
		if isNew {
			fmt.Printf("已收进 Codex 账号 %s（%s）\n", acc.ID, acc.Email)
		} else {
			fmt.Printf("已更新 Codex 账号 %s（%s）\n", acc.ID, acc.Email)
		}
		return nil
	})
	return err
}

func importGemini() error {
	_, err := core.Mutate(func(s *core.Store) error {
		home := core.GeminiHome(s.Settings)
		idty, ok := gemini.ReadIdentity(home)
		if !ok {
			return fmt.Errorf("当前 Gemini 没有会员登录")
		}
		for i := range s.Accounts {
			a := &s.Accounts[i]
			if a.Kind == core.KindGemini && (a.RemoteID == idty.ID && idty.ID != "" || a.Email == idty.Email && idty.Email != "" || filepath.Clean(a.ConfigDir) == filepath.Clean(home)) {
				a.Email = idty.Email
				a.RemoteID = idty.ID
				a.ConfigDir = home
				fmt.Printf("已更新 Gemini 账号 %s（%s）\n", a.ID, or(a.Email, a.ID))
				return nil
			}
		}
		label := idty.Email
		if label == "" {
			label = "gemini"
		}
		id := s.NewID(core.KindGemini, label)
		s.Accounts = append(s.Accounts, core.Account{
			ID: id, Kind: core.KindGemini, Label: label, Email: idty.Email,
			RemoteID: idty.ID, ConfigDir: home, CreatedAt: time.Now(),
		})
		fmt.Printf("已收进 Gemini 账号 %s（%s）\n", id, or(idty.Email, id))
		return nil
	})
	return err
}

func importGrok() error {
	_, err := core.Mutate(func(s *core.Store) error {
		home := core.GrokHome(s.Settings)
		idty, ok := grok.ReadIdentity(home)
		if !ok {
			return fmt.Errorf("当前 Grok 没有会员登录")
		}
		for i := range s.Accounts {
			a := &s.Accounts[i]
			if a.Kind == core.KindGrok && (a.RemoteID == idty.UserID && idty.UserID != "" || a.Email == idty.Email && idty.Email != "" || filepath.Clean(a.ConfigDir) == filepath.Clean(home)) {
				a.Email = idty.Email
				a.RemoteID = idty.UserID
				a.ConfigDir = home
				fmt.Printf("已更新 Grok 账号 %s（%s）\n", a.ID, or(a.Email, a.ID))
				return nil
			}
		}
		label := idty.Email
		if label == "" {
			label = "grok"
		}
		id := s.NewID(core.KindGrok, label)
		s.Accounts = append(s.Accounts, core.Account{
			ID: id, Kind: core.KindGrok, Label: label, Email: idty.Email,
			RemoteID: idty.UserID, ConfigDir: home, CreatedAt: time.Now(),
		})
		fmt.Printf("已收进 Grok 账号 %s（%s）\n", id, or(idty.Email, id))
		return nil
	})
	return err
}

func importAgy(label string) error {
	_, err := core.Mutate(func(s *core.Store) error {
		home := core.AgyHome(s.Settings)
		idty, ok := agy.ReadIdentity(home)
		if !ok {
			return fmt.Errorf("当前 Antigravity 没有登录。gcccode account add agy 会先带你登录")
		}
		for i := range s.Accounts {
			a := &s.Accounts[i]
			if a.Kind == core.KindAgy && (a.RemoteID == idty.UserID && idty.UserID != "" || a.Email == idty.Email && idty.Email != "" || filepath.Clean(a.ConfigDir) == filepath.Clean(home)) {
				a.Email = idty.Email
				a.RemoteID = idty.UserID
				a.ConfigDir = home
				fmt.Printf("已更新 Antigravity 账号 %s（%s）\n", a.ID, or(a.Email, a.ID))
				return nil
			}
		}
		if label == "" {
			label = idty.Email
		}
		if label == "" {
			label = "agy"
		}
		id := s.NewID(core.KindAgy, label)
		s.Accounts = append(s.Accounts, core.Account{
			ID: id, Kind: core.KindAgy, Label: label, Email: idty.Email,
			RemoteID: idty.UserID, ConfigDir: home, CreatedAt: time.Now(),
		})
		fmt.Printf("已收进 Antigravity 账号 %s（%s）\n", id, or(idty.Email, id))
		return nil
	})
	return err
}

func loginAccount(id string) error {
	st, err := core.Load()
	if err != nil {
		return err
	}
	a, ok := st.Account(id)
	if !ok {
		return fmt.Errorf("没有这个账号：%s", id)
	}
	switch a.Kind {
	case core.KindClaude:
		if err := claude.EnsureProfile(st.Settings, a.ConfigDir); err != nil {
			return err
		}
		if err := runClaudeLogin(*a); err != nil {
			return err
		}
		return refreshClaudeIdentity(id)
	case core.KindCodex:
		return codexLogin(id, a.Label)
	case core.KindGemini:
		if err := gemini.EnsureProfile(st.Settings, a.ConfigDir); err != nil {
			return err
		}
		if err := runGeminiLogin(*a); err != nil {
			return err
		}
		return refreshGeminiIdentity(id)
	case core.KindGrok:
		if err := grok.EnsureProfile(st.Settings, a.ConfigDir); err != nil {
			return err
		}
		if err := runGrokLogin(*a); err != nil {
			return err
		}
		return refreshGrokIdentity(id)
	case core.KindAgy:
		if err := runAgyLogin(*a); err != nil {
			return err
		}
		return refreshAgyIdentity(id)
	}
	return nil
}

func runClaudeLogin(acc core.Account) error {
	st, err := core.Load()
	if err != nil {
		return err
	}
	bin, err := bins.Claude(st.Settings)
	if err != nil {
		return fmt.Errorf("找不到 claude：%w", err)
	}
	cmd := exec.Command(bin, "auth", "login")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = launcher.ChildEnv(st.Settings, acc)
	return cmd.Run()
}

func refreshClaudeIdentity(id string) error {
	_, err := core.Mutate(func(s *core.Store) error {
		a, ok := s.Account(id)
		if !ok {
			return nil
		}
		idty, ok := claude.ReadIdentity(s.Settings, a.ConfigDir)
		if !ok {
			fmt.Printf("账号 %s 还没有登录成功。可以再运行 gcccode account login %s\n", id, id)
			return nil
		}
		a.Email = idty.Email
		a.RemoteID = idty.AccountUUID
		a.Quarantined = false
		a.QuarantineReason = ""
		if a.Label == a.ID || a.Label == "" {
			a.Label = idty.Email
		}
		fmt.Printf("Claude 账号 %s 已登录：%s\n", a.ID, a.Email)
		return nil
	})
	return err
}

func runGeminiLogin(acc core.Account) error {
	st, err := core.Load()
	if err != nil {
		return err
	}
	bin, err := bins.Gemini(st.Settings)
	if err != nil {
		return fmt.Errorf("找不到 gemini：%w", err)
	}
	for _, args := range [][]string{{"auth", "login"}, {"login"}, {}} {
		c := exec.Command(bin, args...)
		c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
		c.Env = launcher.ChildEnv(st.Settings, acc)
		if err := c.Run(); err == nil {
			return nil
		} else if len(args) == 0 {
			return err
		}
	}
	return nil
}

func refreshGeminiIdentity(id string) error {
	_, err := core.Mutate(func(s *core.Store) error {
		a, ok := s.Account(id)
		if !ok {
			return nil
		}
		idty, ok := gemini.ReadIdentity(a.ConfigDir)
		if !ok {
			fmt.Printf("账号 %s 还没有登录成功。可以再运行 gcccode account login %s\n", id, id)
			return nil
		}
		a.Email = idty.Email
		a.RemoteID = idty.ID
		a.Quarantined = false
		a.QuarantineReason = ""
		if (a.Label == a.ID || a.Label == "") && idty.Email != "" {
			a.Label = idty.Email
		}
		fmt.Printf("Gemini 账号 %s 已登录：%s\n", a.ID, or(a.Email, a.ID))
		return nil
	})
	return err
}

func runGrokLogin(acc core.Account) error {
	st, err := core.Load()
	if err != nil {
		return err
	}
	bin, err := bins.Grok(st.Settings)
	if err != nil {
		return fmt.Errorf("找不到 grok：%w", err)
	}
	for _, args := range [][]string{{"login"}, {}} {
		c := exec.Command(bin, args...)
		c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
		c.Env = launcher.ChildEnv(st.Settings, acc)
		if err := c.Run(); err == nil {
			return nil
		} else if len(args) == 0 {
			return err
		}
	}
	return nil
}

func refreshGrokIdentity(id string) error {
	_, err := core.Mutate(func(s *core.Store) error {
		a, ok := s.Account(id)
		if !ok {
			return nil
		}
		idty, ok := grok.ReadIdentity(a.ConfigDir)
		if !ok {
			fmt.Printf("账号 %s 还没有登录成功。可以再运行 gcccode account login %s\n", id, id)
			return nil
		}
		a.Email = idty.Email
		a.RemoteID = idty.UserID
		a.Quarantined = false
		a.QuarantineReason = ""
		if (a.Label == a.ID || a.Label == "") && idty.Email != "" {
			a.Label = idty.Email
		}
		fmt.Printf("Grok 账号 %s 已登录：%s\n", a.ID, or(a.Email, a.ID))
		return nil
	})
	return err
}

// runAgyLogin starts agy, which has no login command and signs in on first
// launch. Leaving with Ctrl+C after signing in still counts.
func runAgyLogin(acc core.Account) error {
	st, err := core.Load()
	if err != nil {
		return err
	}
	bin, err := bins.Agy(st.Settings)
	if err != nil {
		return fmt.Errorf("找不到 agy：%w", err)
	}
	if agy.Ready(acc.ConfigDir) {
		fmt.Println("agy 已经登录。要换 Google 账号，在 agy 里输入 /logout 再重新登录；不换就输入 /exit。")
	} else {
		fmt.Println("agy 会给出 Google 登录链接。登录后看到输入框，输入 /exit 退出。")
	}
	c := exec.Command(bin)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	c.Env = launcher.ChildEnv(st.Settings, acc)
	runErr := c.Run()
	if agy.Ready(acc.ConfigDir) {
		return nil
	}
	return runErr
}

func refreshAgyIdentity(id string) error {
	_, err := core.Mutate(func(s *core.Store) error {
		a, ok := s.Account(id)
		if !ok {
			return nil
		}
		idty, ok := agy.ReadIdentity(a.ConfigDir)
		if !ok {
			fmt.Printf("账号 %s 还没有登录成功。可以再运行 gcccode account login %s\n", id, id)
			return nil
		}
		a.Email = idty.Email
		a.RemoteID = idty.UserID
		a.Quarantined = false
		a.QuarantineReason = ""
		if (a.Label == a.ID || a.Label == "") && idty.Email != "" {
			a.Label = idty.Email
		}
		fmt.Printf("Antigravity 账号 %s 已登录：%s\n", a.ID, or(a.Email, a.ID))
		return nil
	})
	return err
}

func codexLogin(id, label string) error {
	if procs := codex.Running(); len(procs) > 0 {
		return &codex.RunningError{Procs: procs}
	}
	st, err := core.Load()
	if err != nil {
		return err
	}
	if _, err := core.Mutate(func(s *core.Store) error {
		_, _, err := codex.SyncBack(s)
		return err
	}); err != nil {
		return fmt.Errorf("写回当前登录失败，已中止：%w", err)
	}
	st, _ = core.Load()
	bin, err := bins.Codex(st.Settings)
	if err != nil {
		return fmt.Errorf("找不到 codex：%w", err)
	}
	live := codex.LivePath(st.Settings)
	backup, _ := os.ReadFile(live)
	_ = os.Remove(live)
	cmd := exec.Command(bin, "login")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	runErr := cmd.Run()
	auth, readErr := codex.ReadAuthFile(live)
	if readErr != nil || !auth.HasRefresh {
		if len(backup) > 0 {
			_ = core.WriteFileAtomic(live, backup, 0o600)
		}
		if runErr != nil {
			return runErr
		}
		return fmt.Errorf("登录没有完成，已恢复原来的号")
	}
	_, err = core.Mutate(func(s *core.Store) error {
		acc, isNew, err := codex.Import(s, auth, label)
		if err != nil {
			return err
		}
		if id != "" && acc.ID != id && !isNew {
			fmt.Printf("登录的是已有账号 %s，不是 %s\n", acc.ID, id)
		}
		s.State.CodexCurrent = acc.ID
		s.State.CodexSince = time.Now()
		if a, ok := s.Account(acc.ID); ok {
			a.Quarantined = false
			a.QuarantineReason = ""
		}
		fmt.Printf("Codex 账号 %s 已登录：%s\n", acc.ID, acc.Email)
		return nil
	})
	return err
}

func removeAccount(id string) error {
	_, err := core.Mutate(func(s *core.Store) error {
		a, ok := s.Account(id)
		if !ok {
			return fmt.Errorf("没有这个账号：%s", id)
		}
		if a.Kind == core.KindCodex {
			dst := filepath.Join(core.BackupDir(), "removed-"+id+"-"+time.Now().Format("20060102-150405"))
			_ = os.MkdirAll(filepath.Dir(dst), 0o700)
			_ = os.Rename(core.CodexVaultDir(id), dst)
		}
		next := s.Accounts[:0]
		for _, item := range s.Accounts {
			if item.ID != id {
				next = append(next, item)
			}
		}
		s.Accounts = next
		delete(s.Quota, id)
		delete(s.State.Occupied, id)
		delete(s.State.Cooldown, id)
		if s.State.CodexCurrent == id {
			s.State.CodexCurrent = ""
		}
		fmt.Printf("已从号池移除 %s。聊天记录还在原来的地方。\n", id)
		return nil
	})
	return err
}

func setProxy(id, value string) error {
	_, err := core.Mutate(func(s *core.Store) error {
		a, ok := s.Account(id)
		if !ok {
			return fmt.Errorf("没有这个账号：%s", id)
		}
		if value == "off" || value == "-" {
			a.Proxy = ""
		} else {
			switch a.Kind {
			case core.KindClaude:
				if msg := claude.ProxyProblem(value); msg != "" {
					return fmt.Errorf("%s", msg)
				}
			case core.KindGemini:
				if msg := gemini.ProxyProblem(value); msg != "" {
					return fmt.Errorf("%s", msg)
				}
			case core.KindGrok:
				if msg := grok.ProxyProblem(value); msg != "" {
					return fmt.Errorf("%s", msg)
				}
			case core.KindAgy:
				if msg := agy.ProxyProblem(value); msg != "" {
					return fmt.Errorf("%s", msg)
				}
			}
			a.Proxy = value
		}
		fmt.Printf("%s 的出口代理：%s\n", id, or(a.Proxy, "跟随系统"))
		return nil
	})
	return err
}

func setFlag(action, id string) error {
	_, err := core.Mutate(func(s *core.Store) error {
		a, ok := s.Account(id)
		if !ok {
			return fmt.Errorf("没有这个账号：%s", id)
		}
		switch action {
		case "disable":
			a.Disabled = true
		case "enable":
			a.Disabled = false
		case "unquarantine":
			a.Quarantined = false
			a.QuarantineReason = ""
		}
		return nil
	})
	return err
}

func cmdUse(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("用法：gcccode use claude|codex|gemini|grok|agy <id>")
	}
	kind, id := args[0], args[1]
	_, err := core.Mutate(func(s *core.Store) error {
		a, ok := s.Account(id)
		if !ok || a.Kind != kind {
			return fmt.Errorf("没有这个 %s 账号：%s", kind, id)
		}
		if s.Settings.Manual == nil {
			s.Settings.Manual = map[string]string{}
		}
		s.Settings.Manual[kind] = id
		if kind == core.KindCodex {
			if err := codex.Activate(s, id, false); err != nil {
				return err
			}
			fmt.Println("Codex 桌面版如果正开着，重新打开后才会换成这个号。")
		}
		if s.Settings.Strategy != "manual" {
			fmt.Printf("已记下 %s。当前策略是 %s，启动时仍会自动选号。要固定用它：gcccode strategy manual\n", id, s.Settings.Strategy)
		} else {
			fmt.Printf("已指定 %s 使用 %s\n", kind, id)
		}
		return nil
	})
	return err
}

func accountReady(st *core.Store) scheduler.Ready {
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

func or(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
