package web

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/nearmetips/DailiCode/internal/bins"
	"github.com/nearmetips/DailiCode/internal/browser"
	"github.com/nearmetips/DailiCode/internal/claude"
	"github.com/nearmetips/DailiCode/internal/core"
	"github.com/nearmetips/DailiCode/internal/setuptoken"
)

// setupTimeout bounds one `claude setup-token` run: signing in and clicking
// authorize takes minutes, not hours.
const setupTimeout = 15 * time.Minute

// setupJob is one in-panel Claude add: `claude setup-token` running in the
// background while the user authorizes in the account's own browser.
type setupJob struct {
	mu      sync.Mutex
	acc     core.Account
	state   string // running, done, failed
	message string
	cancel  context.CancelFunc
}

type setupStatus struct {
	Active  bool   `json:"active"`
	State   string `json:"state,omitempty"`
	Label   string `json:"label,omitempty"`
	ID      string `json:"id,omitempty"`
	Message string `json:"message,omitempty"`
	Days    int    `json:"days,omitempty"`
}

var setup struct {
	mu  sync.Mutex
	job *setupJob
}

// capture runs setup-token; tests swap it for a stand-in.
var capture = setuptoken.Capture

func (j *setupJob) status() setupStatus {
	j.mu.Lock()
	defer j.mu.Unlock()
	return setupStatus{Active: true, State: j.state, Label: j.acc.Label, ID: j.acc.ID, Message: j.message, Days: int(claude.TokenLifetime.Hours() / 24)}
}

func (j *setupJob) finish(state, message string) {
	j.mu.Lock()
	j.state, j.message = state, message
	j.mu.Unlock()
}

// handleSetup starts adding a Claude account. With a token in the body the
// account is created at once; otherwise `claude setup-token` runs in the
// background and the page polls handleSetupStatus.
func handleSetup(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Label string `json:"label"`
		Proxy string `json:"proxy"`
		Token string `json:"token"`
	}
	if !decode(w, r, &body) {
		return
	}
	st, err := core.Load()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if _, err := bins.Claude(st.Settings); err != nil {
		http.Error(w, "这台电脑上没找到 Claude Code，先安装它再加号", http.StatusBadRequest)
		return
	}
	body.Proxy = strings.TrimSpace(body.Proxy)
	if body.Proxy != "" {
		if msg := claude.ProxyProblem(body.Proxy); msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}
	}
	acc := setuptoken.NewAccount(st, strings.TrimSpace(body.Label), body.Proxy)
	if strings.TrimSpace(body.Token) != "" {
		tok := claude.CleanToken(body.Token)
		if msg := claude.TokenProblem(tok); msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}
		if err := claude.EnsureProfile(st.Settings, acc.ConfigDir); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := setuptoken.Save(acc, tok); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, setupStatus{Active: false, State: "done", Label: acc.Label, ID: acc.ID,
			Message: fmt.Sprintf("已加入号池：%s。token 大约 %d 天后过期，快到期时这里会提醒你。", acc.Label, int(claude.TokenLifetime.Hours()/24))})
		return
	}

	setup.mu.Lock()
	if j := setup.job; j != nil && j.status().State == "running" {
		setup.mu.Unlock()
		http.Error(w, "正在加另一个号（"+j.acc.Label+"），等它完成或先取消。", http.StatusConflict)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), setupTimeout)
	j := &setupJob{acc: acc, state: "running", cancel: cancel}
	setup.job = j
	setup.mu.Unlock()

	run := capture
	go func() {
		defer cancel()
		tok, err := run(ctx, st.Settings, acc)
		if err == nil {
			err = setuptoken.Save(acc, tok)
		}
		if err != nil {
			setuptoken.Discard(acc.ID)
			msg := err.Error()
			if ctx.Err() == context.DeadlineExceeded {
				msg = "等了 15 分钟还没授权，这次先停了。再点一次「添加」重新来。"
			}
			j.finish("failed", msg)
			return
		}
		j.finish("done", fmt.Sprintf("已加入号池：%s。token 大约 %d 天后过期，快到期时这里会提醒你。", acc.Label, int(claude.TokenLifetime.Hours()/24)))
	}()

	hint := "专用浏览器已经打开。在那个窗口里登录这个 Claude 号，点 Authorize（授权），这里会自动完成。"
	if !browser.Available() {
		hint = "没找到 Chrome、Edge 或 Brave。请在浏览器里新建一个个人资料，然后在终端运行 gcccode account add claude，把它打印的链接复制到那个新资料的窗口里打开。"
	}
	writeJSON(w, setupStatus{Active: true, State: "running", Label: acc.Label, ID: acc.ID, Message: hint})
}

func handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	setup.mu.Lock()
	j := setup.job
	setup.mu.Unlock()
	if j == nil {
		writeJSON(w, setupStatus{Active: false})
		return
	}
	writeJSON(w, j.status())
}

func handleSetupCancel(w http.ResponseWriter, r *http.Request) {
	var body struct{}
	if !decode(w, r, &body) {
		return
	}
	setup.mu.Lock()
	j := setup.job
	setup.job = nil
	setup.mu.Unlock()
	if j != nil {
		j.cancel()
	}
	writeJSON(w, map[string]any{"ok": true})
}
