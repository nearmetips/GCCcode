package agy

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nearmetips/DailiCode/internal/safety"
)

func glog(level string, at time.Time, msg string) string {
	return fmt.Sprintf("ERROR: logging before google.Init: %s%s %s      42 server.go:190] %s",
		level, at.Format("0102"), at.Format("15:04:05.000000"), msg)
}

func writeLog(t *testing.T, home string, lines ...string) {
	t.Helper()
	dir := filepath.Join(cliDir(home), "log")
	os.MkdirAll(dir, 0o755)
	if err := os.WriteFile(filepath.Join(dir, "cli-1.log"), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestScanSignalsFindsASpentQuotaOnErrorLinesOnly(t *testing.T) {
	home := t.TempDir()
	started := time.Now().Add(-time.Minute)
	writeLog(t, home,
		glog("I", started.Add(time.Second), "doRefreshQuota: starting reload (force=true)"),
		glog("E", started.Add(-time.Hour), "RESOURCE_EXHAUSTED: Allocation Quota Reached"),
		glog("W", started.Add(2*time.Second), "RESOURCE_EXHAUSTED: no model capacity available, try again later"),
		glog("E", started.Add(3*time.Second), "Your image generation quota has been exceeded. Please try again later."),
	)
	if sig := ScanSignals(home, started); sig.Kind != safety.None {
		t.Fatalf("refresh lines, old errors, capacity and image limits must not count: %+v", sig)
	}
	writeLog(t, home,
		glog("I", started.Add(time.Second), "doRefreshQuota: starting reload (force=true)"),
		glog("E", started.Add(5*time.Second), "stream failed: rpc error: code = ResourceExhausted desc = RESOURCE_EXHAUSTED: Allocation Quota Reached"),
	)
	sig := ScanSignals(home, started)
	if sig.Kind != safety.Limit || !strings.Contains(sig.Reason, "Allocation Quota Reached") {
		t.Fatalf("quota error missed: %+v", sig)
	}
	if sig.At.IsZero() || sig.At.Before(started) {
		t.Fatalf("signal time %v", sig.At)
	}
}

func TestScanSignalsFindsARevokedLogin(t *testing.T) {
	home := t.TempDir()
	started := time.Now().Add(-time.Minute)
	writeLog(t, home, glog("E", started.Add(time.Second), `oauth2: "invalid_grant" "Token has been expired or revoked."`))
	if sig := ScanSignals(home, started); sig.Kind != safety.NeedsLogin {
		t.Fatalf("revoked login missed: %+v", sig)
	}
}

func TestLogTimeWrapsTheYear(t *testing.T) {
	now := time.Date(2027, 1, 1, 0, 5, 0, 0, time.Local)
	got := logTime("1231", "23:59:00.000000", now)
	if got.Year() != 2026 {
		t.Fatalf("a December line read on Jan 1 should be last year: %v", got)
	}
}
