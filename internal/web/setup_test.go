package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nearmetips/DailiCode/internal/core"
)

const fakeToken = "sk-ant-oat01-" + "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789abcdef"

// stubSetup gives the panel a Claude binary and a stand-in for setup-token.
func stubSetup(t *testing.T, run func(context.Context, core.Settings, core.Account) (string, error)) {
	t.Helper()
	stubActions(t)
	fake := filepath.Join(t.TempDir(), "claude")
	_ = os.WriteFile(fake, []byte("#!/bin/sh\nexit 0\n"), 0o755)
	if _, err := core.Mutate(func(s *core.Store) error {
		s.Settings.ClaudeBin = fake
		s.Settings.ClaudeHome = t.TempDir()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	old := capture
	capture = run
	t.Cleanup(func() {
		capture = old
		setup.mu.Lock()
		setup.job = nil
		setup.mu.Unlock()
	})
}

func status(t *testing.T) setupStatus {
	t.Helper()
	rec := httptest.NewRecorder()
	handleSetupStatus(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	var s setupStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func waitFor(t *testing.T, state string) setupStatus {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if s := status(t); s.State == state {
			return s
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("job never reached %s: %+v", state, status(t))
	return setupStatus{}
}

func TestSetupAddsTheAccountOnceAuthorized(t *testing.T) {
	release := make(chan struct{})
	var got core.Account
	stubSetup(t, func(ctx context.Context, s core.Settings, acc core.Account) (string, error) {
		got = acc
		select {
		case <-release:
			return fakeToken, nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	})
	rec := post(handleSetup, `{"label":"三号","proxy":"http://127.0.0.1:7893"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if s := status(t); !s.Active || s.State != "running" || s.Label != "三号" {
		t.Fatalf("status %+v", s)
	}
	if rec := post(handleSetup, `{"label":"四号"}`); rec.Code != http.StatusConflict {
		t.Fatalf("a second add during the first: %d", rec.Code)
	}
	close(release)
	done := waitFor(t, "done")
	st, _ := core.Load()
	a, ok := st.Account(done.ID)
	if !ok || a.Label != "三号" || a.Proxy != "http://127.0.0.1:7893" || a.TokenAt.IsZero() {
		t.Fatalf("saved %+v", a)
	}
	if got.ID != a.ID || got.ConfigDir != core.ClaudeProfileDir(a.ID) {
		t.Fatalf("setup-token ran for %+v", got)
	}
	b, _ := os.ReadFile(core.ClaudeTokenFile(a.ID))
	if strings.TrimSpace(string(b)) != fakeToken {
		t.Fatalf("token file %q", b)
	}
}

func TestSetupRejectsBadInputBeforeRunningAnything(t *testing.T) {
	ran := false
	stubSetup(t, func(context.Context, core.Settings, core.Account) (string, error) { ran = true; return fakeToken, nil })
	if rec := post(handleSetup, `{"label":"x","proxy":"socks5://127.0.0.1:1080"}`); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "SOCKS") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := post(handleSetup, `{"label":"x","token":"sk-ant-api03-`+strings.Repeat("k", 60)+`"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("API key accepted: %d %s", rec.Code, rec.Body)
	}
	if ran {
		t.Fatal("setup-token ran for rejected input")
	}
}

func TestSetupWithAPastedTokenNeedsNoBrowser(t *testing.T) {
	stubSetup(t, func(context.Context, core.Settings, core.Account) (string, error) {
		t.Fatal("setup-token must not run when the token was pasted")
		return "", nil
	})
	rec := post(handleSetup, `{"label":"旧号","token":"export CLAUDE_CODE_OAUTH_TOKEN=`+fakeToken+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var s setupStatus
	_ = json.Unmarshal(rec.Body.Bytes(), &s)
	if s.State != "done" || s.ID == "" {
		t.Fatalf("%+v", s)
	}
	st, _ := core.Load()
	if a, ok := st.Account(s.ID); !ok || a.TokenAt.IsZero() {
		t.Fatalf("account %+v", a)
	}
}

func TestSetupFailureCleansUpAndReports(t *testing.T) {
	stubSetup(t, func(ctx context.Context, s core.Settings, acc core.Account) (string, error) {
		_ = os.MkdirAll(acc.ConfigDir, 0o700)
		return "", errors.New("没找到 token。claude setup-token 的输出：\nOAuth error: access_denied")
	})
	rec := post(handleSetup, `{"label":"坏号"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	failed := waitFor(t, "failed")
	if !strings.Contains(failed.Message, "access_denied") {
		t.Fatalf("message %q", failed.Message)
	}
	st, _ := core.Load()
	if _, ok := st.Account(failed.ID); ok {
		t.Fatal("failed add left an account behind")
	}
	if _, err := os.Stat(core.ClaudeProfileDir(failed.ID)); !os.IsNotExist(err) {
		t.Fatal("failed add left its profile behind")
	}
	// A new add may start right after a failure.
	if rec := post(handleSetup, `{"label":"再来"}`); rec.Code != http.StatusOK {
		t.Fatalf("retry: %d %s", rec.Code, rec.Body)
	}
	waitFor(t, "failed")
}

func TestSetupCancelStopsTheRun(t *testing.T) {
	cancelled := make(chan struct{})
	stubSetup(t, func(ctx context.Context, s core.Settings, acc core.Account) (string, error) {
		<-ctx.Done()
		close(cancelled)
		return "", ctx.Err()
	})
	if rec := post(handleSetup, `{"label":"半途"}`); rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := post(handleSetupCancel, `{}`); rec.Code != http.StatusOK {
		t.Fatalf("cancel: %d %s", rec.Code, rec.Body)
	}
	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("setup-token kept running after cancel")
	}
	if s := status(t); s.Active {
		t.Fatalf("cancelled job still reported: %+v", s)
	}
}

func TestCLIInstallLinksThisBinary(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SHELL", "/bin/zsh")
	old := shellSees
	shellSees = func(string) bool { return false }
	t.Cleanup(func() { shellSees = old })

	if cliStatus().Installed {
		t.Fatal("installed before linking")
	}
	msg, err := cliInstall()
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(home, ".local", "bin", "gcccode")
	self, _ := os.Executable()
	self, _ = filepath.EvalSymlinks(self)
	if target, _ := filepath.EvalSymlinks(link); target != self {
		t.Fatalf("link points at %q, want %q", target, self)
	}
	if !cliStatus().Installed {
		t.Fatal("not installed after linking")
	}
	prof, _ := os.ReadFile(filepath.Join(home, ".zprofile"))
	if !strings.Contains(string(prof), `.local/bin`) || !strings.Contains(msg, ".zprofile") {
		t.Fatalf("profile %q, msg %q", prof, msg)
	}
	// Running it again must not add a second PATH line.
	if _, err := cliInstall(); err != nil {
		t.Fatal(err)
	}
	prof2, _ := os.ReadFile(filepath.Join(home, ".zprofile"))
	if strings.Count(string(prof2), ".local/bin") != 1 {
		t.Fatalf("profile written twice: %q", prof2)
	}
}
