//go:build !nogui

package tray

import (
	"fmt"
	"net"
	"time"

	"fyne.io/systray"

	"github.com/nearmetips/DailiCode/internal/agy"
	"github.com/nearmetips/DailiCode/internal/claude"
	"github.com/nearmetips/DailiCode/internal/codex"
	"github.com/nearmetips/DailiCode/internal/core"
	"github.com/nearmetips/DailiCode/internal/gemini"
	"github.com/nearmetips/DailiCode/internal/grok"
	"github.com/nearmetips/DailiCode/internal/scheduler"
	"github.com/nearmetips/DailiCode/internal/web"
)

// Run blocks on the menu bar until the user quits it.
func Run() error {
	systray.Run(onReady, func() {})
	return nil
}

func onReady() {
	systray.SetTitle("GCC")
	systray.SetTooltip("GCC Code")
	systray.SetIcon(iconPNG)
	status := systray.AddMenuItem("正在读取账号…", "")
	status.Disable()
	systray.AddSeparator()
	var slots [6]*systray.MenuItem
	for i := range slots {
		slots[i] = systray.AddMenuItem("", "")
		slots[i].Hide()
	}
	systray.AddSeparator()
	open := systray.AddMenuItem("打开面板", "")
	quit := systray.AddMenuItem("退出", "")

	go func() {
		tick := time.NewTicker(20 * time.Second)
		defer tick.Stop()
		refresh(status, slots[:])
		for {
			select {
			case <-tick.C:
				refresh(status, slots[:])
			case <-open.ClickedCh:
				go openPanel()
			case <-quit.ClickedCh:
				systray.Quit()
				return
			}
		}
	}()
	for i := range slots {
		go func(item *systray.MenuItem) {
			for range item.ClickedCh {
				openPanel()
			}
		}(slots[i])
	}
}

func refresh(status *systray.MenuItem, slots []*systray.MenuItem) {
	st, err := core.Load()
	if err != nil {
		status.SetTitle("读取失败")
		return
	}
	st.PruneOccupancy()
	now := time.Now()
	ready := func(a core.Account) bool {
		switch a.Kind {
		case core.KindClaude:
			return claude.Ready(st.Settings, a)
		case core.KindCodex:
			return fileOK(codex.VaultPath(a.ID))
		case core.KindGemini:
			return gemini.Ready(a.ConfigDir)
		case core.KindGrok:
			return grok.Ready(a.ConfigDir)
		case core.KindAgy:
			return agy.Ready(a.ConfigDir)
		}
		return false
	}
	var lines []string
	for _, kind := range core.Kinds {
		for _, s := range scheduler.Evaluate(st, kind, ready, now) {
			name := s.Account.Label
			if name == "" {
				name = s.Account.ID
			}
			pct := ""
			if s.HasQuota && s.Quota.FiveHour != nil {
				pct = fmt.Sprintf(" 5h %.0f%%", s.Quota.FiveHour.UsedPct)
			}
			lines = append(lines, name+pct+" · "+s.Reason)
		}
	}
	if len(lines) == 0 {
		status.SetTitle("还没有账号")
	} else {
		status.SetTitle(fmt.Sprintf("%d 个账号", len(lines)))
	}
	for i, item := range slots {
		if i < len(lines) {
			item.SetTitle(lines[i])
			item.Show()
		} else {
			item.Hide()
		}
	}
}

func openPanel() {
	st, err := core.Load()
	addr := "127.0.0.1:4725"
	if err == nil {
		addr = web.Addr(st.Settings)
	}
	if !listening(addr) {
		go func() { _ = web.Serve(addr) }()
		time.Sleep(200 * time.Millisecond)
	}
	web.OpenBrowser("http://" + addr)
}

func listening(addr string) bool {
	c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

func fileOK(p string) bool {
	_, err := stat(p)
	return err == nil
}
