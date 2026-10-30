package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
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
	"github.com/nearmetips/DailiCode/internal/integrate"
)

const codexBusy = "Codex 还开着（ChatGPT 桌面版里的 Codex 也算）。先完全退出它，再点一次。不然它会把旧账号写回去。"

type clientView struct {
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Installed  bool   `json:"installed"`
	InstallURL string `json:"install_url,omitempty"`
	Count      int    `json:"count"`
	CanAdd     bool   `json:"can_add"`
	Note       string `json:"note,omitempty"`
	Found      bool   `json:"found"`
	FoundEmail string `json:"found_email,omitempty"`
}

var clientNames = map[string]string{
	core.KindClaude: "Claude Code",
	core.KindCodex:  "Codex（ChatGPT）",
	core.KindGemini: "Gemini CLI",
	core.KindGrok:   "Grok CLI",
	core.KindAgy:    "Antigravity",
}

var installURLs = map[string]string{
	core.KindClaude: "https://github.com/anthropics/claude-code",
	core.KindCodex:  "https://github.com/openai/codex",
	core.KindGemini: "https://github.com/google-gemini/gemini-cli",
}

func clients(st *core.Store) []clientView {
	out := make([]clientView, 0, len(core.Kinds))
	for _, kind := range core.Kinds {
		c := clientView{Kind: kind, Name: clientNames[kind], InstallURL: installURLs[kind], Count: len(st.ByKind(kind))}
		c.Installed = installed(st.Settings, kind)
		c.CanAdd = c.Installed
		if kind == core.KindClaude {
			c.Note = "每个 Claude 号都有一个专用浏览器，和你平时用的完全分开。在里面登录这个号、点授权，一年期的 token 会自动收好。换号就是换 token。"
		}
		if kind == core.KindAgy && c.Count > 0 {
			c.CanAdd = false
			c.Note = "agy 的登录存在系统钥匙串里一个固定条目，暂时只能放一个号。要换 Google 账号，在下面点「重新登录」。"
		}
		c.FoundEmail, c.Found = unpooled(st, kind)
		out = append(out, c)
	}
	return out
}

func installed(s core.Settings, kind string) bool {
	var err error
	switch kind {
	case core.KindClaude:
		_, err = bins.Claude(s)
	case core.KindCodex:
		_, err = bins.Codex(s)
	case core.KindGemini:
		_, err = bins.Gemini(s)
	case core.KindGrok:
		_, err = bins.Grok(s)
	case core.KindAgy:
		_, err = bins.Agy(s)
	}
	return err == nil
}

// unpooled reports the login a client has on this machine right now when the
// pool does not hold it yet, using the same matching as `account import`.
func unpooled(st *core.Store, kind string) (string, bool) {
	s := st.Settings
	switch kind {
	case core.KindClaude:
		if claude.RelayInSettings(s) {
			return "", false
		}
		if idty, ok := claude.ReadIdentity(s, core.ClaudeHome(s)); ok && !pooled(st, kind, idty.AccountUUID, "", "") {
			return idty.Email, true
		}
	case core.KindCodex:
		a, err := codex.ReadAuthFile(codex.LivePath(s))
		if err != nil || a.Mode == "apikey" || !a.HasRefresh {
			return "", false
		}
		if _, ok := codex.FindByRemote(st, a); !ok {
			return a.Email, true
		}
	case core.KindGemini:
		home := core.GeminiHome(s)
		if idty, ok := gemini.ReadIdentity(home); ok && !pooled(st, kind, idty.ID, idty.Email, home) {
			return idty.Email, true
		}
	case core.KindGrok:
		home := core.GrokHome(s)
		if idty, ok := grok.ReadIdentity(home); ok && !pooled(st, kind, idty.UserID, idty.Email, home) {
			return idty.Email, true
		}
	case core.KindAgy:
		home := core.AgyHome(s)
		if idty, ok := agy.ReadIdentity(home); ok && !pooled(st, kind, idty.UserID, idty.Email, home) {
			return idty.Email, true
		}
	}
	return "", false
}

func pooled(st *core.Store, kind, remote, email, dir string) bool {
	for _, a := range st.ByKind(kind) {
		if remote != "" && a.RemoteID == remote || email != "" && a.Email == email ||
			dir != "" && filepath.Clean(a.ConfigDir) == filepath.Clean(dir) {
			return true
		}
	}
	return false
}

// runSelf runs a gcccode command and returns what it printed, so a button in
// the panel does exactly what the same command does in a terminal.
var runSelf = func(args ...string) (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, self, args...).CombinedOutput()
	msg := strings.TrimSpace(strings.ReplaceAll(string(out), "gcccode: ", ""))
	if err != nil {
		if msg == "" {
			msg = err.Error()
		}
		return "", errors.New(msg)
	}
	return msg, nil
}

var codexRunning = func() bool { return len(codex.Running()) > 0 }

func validKind(kind string) bool {
	for _, k := range core.Kinds {
		if k == kind {
			return true
		}
	}
	return false
}

func account(id string) (core.Account, error) {
	st, err := core.Load()
	if err != nil {
		return core.Account{}, err
	}
	a, ok := st.Account(id)
	if !ok {
		return core.Account{}, fmt.Errorf("没有这个账号：%s", id)
	}
	return *a, nil
}

func handleImport(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Kind string `json:"kind"`
	}
	if !decode(w, r, &body) {
		return
	}
	if !validKind(body.Kind) {
		http.Error(w, "不认识这个客户端", http.StatusBadRequest)
		return
	}
	reply(w)(runSelf("account", "import", body.Kind))
}

// handleLogin opens a terminal that signs in a new account, or signs an
// existing one in again. The panel keeps polling until the account shows up.
func handleLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Kind string `json:"kind"`
		ID   string `json:"id"`
	}
	if !decode(w, r, &body) {
		return
	}
	args := []string{"account", "add", body.Kind}
	if body.ID != "" {
		a, err := account(body.ID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		body.Kind = a.Kind
		args = []string{"account", "login", a.ID}
	} else if !validKind(body.Kind) {
		http.Error(w, "不认识这个客户端", http.StatusBadRequest)
		return
	}
	if body.Kind == core.KindCodex && codexRunning() {
		http.Error(w, codexBusy, http.StatusConflict)
		return
	}
	if err := openTerminal(args...); err != nil {
		http.Error(w, err.Error()+"。请打开终端，手动运行："+commandLine(args), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "message": "已经打开一个终端窗口，跟着提示在浏览器里登录。登录完成后，这里会自动出现这个号。没看到窗口的话，打开终端运行：" + commandLine(args)})
}

func handleRemove(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
	}
	if !decode(w, r, &body) {
		return
	}
	a, err := account(body.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	reply(w)(runSelf("account", "rm", a.ID))
}

func handleProxy(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID    string `json:"id"`
		Proxy string `json:"proxy"`
	}
	if !decode(w, r, &body) {
		return
	}
	a, err := account(body.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	v := strings.TrimSpace(body.Proxy)
	if v == "" {
		v = "off"
	}
	reply(w)(runSelf("account", "proxy", a.ID, v))
}

func handleEnable(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID      string `json:"id"`
		Enabled bool   `json:"enabled"`
	}
	if !decode(w, r, &body) {
		return
	}
	a, err := account(body.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	action, msg := "disable", "已停用 "+a.ID+"，选号时会跳过它。"
	if body.Enabled {
		action, msg = "enable", "已启用 "+a.ID+"。"
	}
	if _, err := runSelf("account", action, a.ID); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "message": msg})
}

func handleContinue(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if !decode(w, r, &body) {
		return
	}
	_, err := core.Mutate(func(s *core.Store) error {
		s.Settings.AutoContinue = body.Enabled
		return nil
	})
	msg := "已关闭自动续接。额度用完退出时，会先问你要不要换号续接。"
	if body.Enabled {
		msg = "已开启自动续接。额度用完退出后，不再询问，直接换下一个号接着刚才的会话。"
	}
	reply(w)(msg, err)
}

func handleStatusLine(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Enabled {
		prev, err := integrate.InstallStatusLine()
		msg := "已接入 Claude Code 状态栏。新开一个 Claude 会话，底部就能看到当前号和额度。"
		if prev != "" {
			msg += "你原来的状态栏照常显示，额度在它下面一行。"
		}
		reply(w)(msg, err)
		return
	}
	removed, err := integrate.RemoveStatusLine()
	msg := "Claude Code 状态栏里本来就没有 GCC Code。"
	if removed {
		msg = "已从 Claude Code 状态栏撤下 GCC Code，原来的状态栏已放回。"
	}
	reply(w)(msg, err)
}

// handleToken saves a pasted setup-token in this process, so the token never
// shows up on a command line.
func handleToken(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	if !decode(w, r, &body) {
		return
	}
	a, err := account(body.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if a.Kind != core.KindClaude {
		http.Error(w, "只有 Claude 号用长期 token", http.StatusBadRequest)
		return
	}
	if err := claude.SaveToken(a.ID, claude.CleanToken(body.Token)); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	_, err = core.Mutate(func(s *core.Store) error {
		acc, ok := s.Account(a.ID)
		if !ok {
			return fmt.Errorf("没有这个账号：%s", a.ID)
		}
		acc.TokenAt = time.Now()
		acc.Quarantined = false
		acc.QuarantineReason = ""
		return nil
	})
	reply(w)(fmt.Sprintf("%s 的 token 已换上，大约 %d 天后过期。", a.Label, int(claude.TokenLifetime.Hours()/24)), err)
}

func handleBrowser(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
	}
	if !decode(w, r, &body) {
		return
	}
	a, err := account(body.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if _, err := runSelf("account", "browser", a.ID); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "message": "已打开 " + a.Label + " 专用的浏览器。只在这个窗口里登录这个号。"})
}

func reply(w http.ResponseWriter) func(string, error) {
	return func(msg string, err error) {
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "message": msg})
	}
}
