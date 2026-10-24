package gateway

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/nearmetips/DailiCode/internal/core"
)

// Process is the running gateway as the pid file records it.
type Process struct {
	PID     int       `json:"pid"`
	Addr    string    `json:"addr"`
	Started time.Time `json:"started"`
}

// WritePID records this process as the running gateway.
func WritePID(addr string) error {
	b, err := json.Marshal(Process{PID: os.Getpid(), Addr: addr, Started: time.Now()})
	if err != nil {
		return err
	}
	return core.WriteFileAtomic(core.GatewayPIDFile(), b, 0o600)
}

// RemovePID drops the pid file, but only when it still names this process.
func RemovePID() {
	if p, ok := Running(); ok && p.PID == os.Getpid() {
		_ = os.Remove(core.GatewayPIDFile())
	}
}

// Running returns the gateway process if the pid file names a live one. A
// stale file left by a crash is removed.
func Running() (Process, bool) {
	b, err := os.ReadFile(core.GatewayPIDFile())
	if err != nil {
		return Process{}, false
	}
	var p Process
	if json.Unmarshal(b, &p) != nil || !core.PIDAlive(p.PID) {
		_ = os.Remove(core.GatewayPIDFile())
		return Process{}, false
	}
	return p, true
}

// NewToken makes the local bearer token agents present to the gateway.
func NewToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "gw-" + hex.EncodeToString(b), nil
}

// Spawn starts `serve` detached from this terminal and waits until the new
// process answers on addr.
func Spawn(exe string, args []string, addr string) (Process, error) {
	logf, err := os.OpenFile(core.GatewayLogFile(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return Process{}, err
	}
	defer logf.Close()
	cmd := exec.Command(exe, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, logf, logf
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return Process{}, err
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-exited:
			return Process{}, fmt.Errorf("网关启动后马上退出了（%v）。日志：%s\n%s", err, core.GatewayLogFile(), tailLog(2048))
		default:
		}
		if resp, err := client.Get("http://" + addr + "/healthz"); err == nil {
			resp.Body.Close()
			if p, ok := Running(); ok {
				return p, nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return Process{}, errors.New("网关 5 秒内没有就绪。日志：" + core.GatewayLogFile())
}

func tailLog(n int64) string {
	f, err := os.Open(core.GatewayLogFile())
	if err != nil {
		return ""
	}
	defer f.Close()
	if fi, err := f.Stat(); err == nil && fi.Size() > n {
		_, _ = f.Seek(fi.Size()-n, io.SeekStart)
	}
	b, _ := io.ReadAll(f)
	return strings.TrimSpace(string(b))
}

// Stop asks the running gateway to exit and waits briefly for it to go.
func Stop() (Process, error) {
	p, ok := Running()
	if !ok {
		return Process{}, errors.New("网关没有在运行")
	}
	if err := terminate(p.PID); err != nil {
		return p, err
	}
	for i := 0; i < 50; i++ {
		if !core.PIDAlive(p.PID) {
			_ = os.Remove(core.GatewayPIDFile())
			return p, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return p, fmt.Errorf("已发出退出信号，但进程 %d 还没退出", p.PID)
}

// FetchStatus asks a running gateway for its provider state.
func FetchStatus(addr, token string) (StatusReport, error) {
	req, err := http.NewRequest(http.MethodGet, "http://"+addr+"/_gcccode/status", nil)
	if err != nil {
		return StatusReport{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
	if err != nil {
		return StatusReport{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return StatusReport{}, fmt.Errorf("网关返回 HTTP %d", resp.StatusCode)
	}
	var rep StatusReport
	return rep, json.NewDecoder(resp.Body).Decode(&rep)
}

// Addr is where the gateway runs, or would run if started now.
func Addr(s core.Settings) string {
	if p, ok := Running(); ok {
		return p.Addr
	}
	if s.Gateway.Addr != "" {
		return s.Gateway.Addr
	}
	return core.DefaultGatewayAddr
}

// PointsAt reports whether raw, a base URL taken from the environment,
// targets the local gateway.
func PointsAt(s core.Settings, raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || !loopbackHost(u.Host) {
		return false
	}
	_, port, err := net.SplitHostPort(Addr(s))
	return err == nil && u.Port() == port
}

// EnvVar is one variable `gateway env` sets.
type EnvVar struct{ Name, Value string }

// Env is what points one protocol's clients at a gateway on addr: that SDK's
// base-URL variable first, then its key variable.
func Env(proto, addr, token string) []EnvVar {
	base := "http://" + addr
	switch proto {
	case ProtoAnthropic:
		return []EnvVar{{"ANTHROPIC_BASE_URL", base}, {"ANTHROPIC_AUTH_TOKEN", token}}
	case ProtoGemini:
		return []EnvVar{{"GOOGLE_GEMINI_BASE_URL", base}, {"GEMINI_API_KEY", token}}
	default:
		return []EnvVar{{"OPENAI_BASE_URL", base + "/v1"}, {"OPENAI_API_KEY", token}}
	}
}

// Inbound lists the inbound protocols cfg's providers can answer: OpenAI
// upstreams serve OpenAI and, translated, Anthropic; the others serve their own.
func Inbound(cfg core.Gateway) []string {
	has := map[string]bool{}
	for _, p := range cfg.Providers {
		if p.Disabled || ValidateProvider(p) != nil {
			continue
		}
		switch {
		case codexSub(p):
			// Codex pool answers Responses, and other agents through translation.
			has[ProtoOpenAI], has[ProtoAnthropic] = true, true
		case claudeSub(p):
			has[ProtoAnthropic] = true
		case Protocol(p) == ProtoOpenAI:
			has[ProtoOpenAI], has[ProtoAnthropic] = true, true
		default:
			has[Protocol(p)] = true
		}
	}
	var out []string
	for _, proto := range []string{ProtoOpenAI, ProtoAnthropic, ProtoGemini} {
		if has[proto] {
			out = append(out, proto)
		}
	}
	return out
}
