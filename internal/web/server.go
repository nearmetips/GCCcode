// Package web serves the local account panel. It binds to loopback only.
package web

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/nearmetips/DailiCode/internal/agy"
	"github.com/nearmetips/DailiCode/internal/claude"
	"github.com/nearmetips/DailiCode/internal/codex"
	"github.com/nearmetips/DailiCode/internal/core"
	"github.com/nearmetips/DailiCode/internal/gemini"
	"github.com/nearmetips/DailiCode/internal/grok"
	"github.com/nearmetips/DailiCode/internal/integrate"
	"github.com/nearmetips/DailiCode/internal/quota"
	"github.com/nearmetips/DailiCode/internal/relay"
	"github.com/nearmetips/DailiCode/internal/scheduler"
)

//go:embed assets/*
var assets embed.FS

// App is set when the panel runs inside the desktop window rather than a
// browser tab. The page then leaves room for the window's title bar.
var App bool

type view struct {
	Version      string        `json:"version"`
	App          bool          `json:"app"`
	CLI          cliView       `json:"cli"`
	Strategy     string        `json:"strategy"`
	AutoContinue bool          `json:"auto_continue"`
	StatusLine   bool          `json:"status_line"`
	Clients      []clientView  `json:"clients"`
	Accounts     []accountView `json:"accounts"`
	Waiting      []string      `json:"waiting"`
	Relay        relayView     `json:"relay"`
	Upsell       bool          `json:"upsell"`
	BuyURL       string        `json:"buy_url"`
}

type accountView struct {
	ID          string       `json:"id"`
	Kind        string       `json:"kind"`
	Label       string       `json:"label"`
	Email       string       `json:"email,omitempty"`
	Plan        string       `json:"plan,omitempty"`
	Reason      string       `json:"reason"`
	Quarantined bool         `json:"quarantined"`
	Disabled    bool         `json:"disabled"`
	Token       bool         `json:"token"`
	TokenDays   int          `json:"token_days,omitempty"`
	Proxy       string       `json:"proxy,omitempty"`
	ProxyNote   string       `json:"proxy_note,omitempty"`
	FiveHour    *core.Window `json:"five_hour,omitempty"`
	Weekly      *core.Window `json:"weekly,omitempty"`
}

type relayView struct {
	Enabled  bool   `json:"enabled"`
	Ready    bool   `json:"ready"`
	Name     string `json:"name"`
	HasToken bool   `json:"has_token"`
}

// Addr resolves the panel address. GCCCODE_ADDR wins over settings.
func Addr(s core.Settings) string {
	return addrOf(s)
}

func addrOf(s core.Settings) string {
	if v := getenv("GCCCODE_ADDR"); v != "" {
		return v
	}
	if s.WebAddr != "" {
		return s.WebAddr
	}
	return "127.0.0.1:4725"
}

func getenv(k string) string {
	return strings.TrimSpace(env(k))
}

// Serve listens until the process exits. addr must be a loopback host. With
// open set, the panel also opens in the default browser once it is listening.
func Serve(addr string, open bool) error {
	ln, err := Listen(addr)
	if err != nil {
		return err
	}
	url := "http://" + ln.Addr().String()
	fmt.Fprintf(stdout(), "面板 %s\n", url)
	if open {
		_ = openBrowser(url)
	}
	return ServeOn(ln)
}

// Listen binds the panel to addr, which must name a loopback host. Port 0
// picks a free port; the listener's Addr tells which.
func Listen(addr string) (net.Listener, error) { return listenLoopback(addr) }

// ServeOn answers panel requests on ln until it closes.
func ServeOn(ln net.Listener) error { return http.Serve(ln, guard(routes())) }

func routes() *http.ServeMux {
	mux := http.NewServeMux()
	sub, _ := fs.Sub(assets, "assets")
	files := http.FileServer(http.FS(sub))
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The assets are built into the binary; after an upgrade the window
		// must not keep showing the old ones.
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	}))
	mux.HandleFunc("/api/state", handleState)
	mux.HandleFunc("/api/strategy", handleStrategy)
	mux.HandleFunc("/api/use", handleUse)
	mux.HandleFunc("/api/unquarantine", handleUnquarantine)
	mux.HandleFunc("/api/relay", handleRelay)
	mux.HandleFunc("/api/import", handleImport)
	mux.HandleFunc("/api/login", handleLogin)
	mux.HandleFunc("/api/remove", handleRemove)
	mux.HandleFunc("/api/proxy", handleProxy)
	mux.HandleFunc("/api/enable", handleEnable)
	mux.HandleFunc("/api/continue", handleContinue)
	mux.HandleFunc("/api/statusline", handleStatusLine)
	mux.HandleFunc("/api/token", handleToken)
	mux.HandleFunc("/api/browser", handleBrowser)
	mux.HandleFunc("/api/claude/setup", handleSetup)
	mux.HandleFunc("/api/claude/setup/status", handleSetupStatus)
	mux.HandleFunc("/api/claude/setup/cancel", handleSetupCancel)
	mux.HandleFunc("/api/cli", handleCLI)
	mux.HandleFunc("/api/usage", handleUsage)
	return mux
}

func listenLoopback(addr string) (net.Listener, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("地址格式应为 host:port：%w", err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return nil, fmt.Errorf("面板只监听本机地址，拒绝 %s", host)
	}
	return net.Listen("tcp", addr)
}

func handleState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	st, err := core.Mutate(func(s *core.Store) error {
		s.PruneOccupancy()
		quota.RefreshCodex(s, time.Now())
		return nil
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, snapshot(st))
}

func snapshot(st *core.Store) view {
	now := time.Now()
	ready := func(a core.Account) bool {
		switch a.Kind {
		case core.KindClaude:
			return claude.Ready(st.Settings, a)
		case core.KindCodex:
			return codexReady(a)
		case core.KindGemini:
			return gemini.Ready(a.ConfigDir)
		case core.KindGrok:
			return grok.Ready(a.ConfigDir)
		case core.KindAgy:
			return agy.Ready(a.ConfigDir)
		}
		return false
	}
	var rows []accountView
	waiting := []string{}
	for _, kind := range core.Kinds {
		all := scheduler.Evaluate(st, kind, ready, now)
		if line := scheduler.NoneUsable(all, now); line != "" {
			waiting = append(waiting, kind+"："+line)
		}
		for _, s := range all {
			label := s.Account.Label
			if label == "" {
				label = s.Account.ID
			}
			row := accountView{
				ID: s.Account.ID, Kind: s.Account.Kind, Label: label, Email: s.Account.Email,
				Plan: s.Account.Plan, Reason: s.Reason, Quarantined: s.Account.Quarantined,
				Disabled: s.Account.Disabled, Proxy: s.Account.Proxy,
			}
			if kind == core.KindClaude {
				if claude.UsesToken(s.Account) {
					row.Token, row.TokenDays = true, claude.TokenDaysLeft(s.Account, now)
				}
				row.Proxy = claude.EffectiveProxy(st.Settings, s.Account)
				row.ProxyNote = panelNote(claude.EgressProblem(st.Settings, st.Accounts, s.Account.ID))
			}
			if s.HasQuota {
				row.FiveHour = s.Quota.FiveHour
				row.Weekly = s.Quota.Weekly
				if row.Plan == "" {
					row.Plan = s.Quota.PlanType
				}
			}
			rows = append(rows, row)
		}
	}
	if rows == nil {
		rows = []accountView{}
	}
	buy := st.Settings.Relay.BuyURL
	if buy == "" {
		buy = "https://gcccode.com/"
	}
	return view{
		Version: core.Version, App: App, CLI: cliStatus(), Strategy: st.Settings.Strategy, Accounts: rows, Waiting: waiting,
		AutoContinue: st.Settings.AutoContinue, StatusLine: integrate.Installed(st.Settings), Clients: clients(st),
		Relay: relayView{
			Enabled: st.Settings.Relay.Enabled, Ready: relay.Ready(st.Settings.Relay),
			Name: st.Settings.Relay.Name, HasToken: st.Settings.Relay.Token != "",
		},
		Upsell: st.Settings.Upsell, BuyURL: buy,
	}
}

func codexReady(a core.Account) bool { return fileExists(codex.VaultPath(a.ID)) }

// panelNote drops the command a note ends with; the panel shows a button for it.
func panelNote(msg string) string {
	if before, _, ok := strings.Cut(msg, "：gcccode "); ok {
		return before
	}
	return msg
}

func handleStrategy(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Strategy string `json:"strategy"`
	}
	if !decode(w, r, &body) {
		return
	}
	switch body.Strategy {
	case "smart", "pace", "manual":
	default:
		http.Error(w, "策略只能是 smart、pace 或 manual", http.StatusBadRequest)
		return
	}
	_, err := core.Mutate(func(s *core.Store) error {
		s.Settings.Strategy = body.Strategy
		return nil
	})
	writeErr(w, err)
}

func handleUse(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Kind string `json:"kind"`
		ID   string `json:"id"`
	}
	if !decode(w, r, &body) {
		return
	}
	_, err := core.Mutate(func(s *core.Store) error {
		a, ok := s.Account(body.ID)
		if !ok || a.Kind != body.Kind {
			return fmt.Errorf("没有这个账号")
		}
		if s.Settings.Manual == nil {
			s.Settings.Manual = map[string]string{}
		}
		s.Settings.Manual[body.Kind] = body.ID
		s.Settings.Strategy = "manual"
		if body.Kind == core.KindCodex {
			return codex.Activate(s, body.ID, false)
		}
		return nil
	})
	writeErr(w, err)
}

func handleUnquarantine(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
	}
	if !decode(w, r, &body) {
		return
	}
	_, err := core.Mutate(func(s *core.Store) error {
		a, ok := s.Account(body.ID)
		if !ok {
			return fmt.Errorf("没有这个账号")
		}
		a.Quarantined = false
		a.QuarantineReason = ""
		return nil
	})
	writeErr(w, err)
}

func handleRelay(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if !decode(w, r, &body) {
		return
	}
	_, err := core.Mutate(func(s *core.Store) error {
		s.Settings.Relay.Enabled = body.Enabled
		return nil
	})
	writeErr(w, err)
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return false
	}
	defer r.Body.Close()
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(v); err != nil {
		http.Error(w, "请求不是合法 JSON", http.StatusBadRequest)
		return false
	}
	return true
}

func writeErr(w http.ResponseWriter, err error) {
	if err != nil {
		var run *codex.RunningError
		if errors.As(err, &run) {
			http.Error(w, codexBusy, http.StatusConflict)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}
