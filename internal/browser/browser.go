// Package browser opens a browser that only one account ever signs in with.
// Each account gets its own user data dir, so its claude.ai login never
// shares cookies with another account or with the user's everyday browser,
// and `claude setup-token` can only authorize the account meant.
package browser

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/nearmetips/DailiCode/internal/core"
)

// ForEnv names the account a URL is for when gcccode itself is the BROWSER a
// client opens links with.
const ForEnv = "GCCCODE_BROWSER_FOR"

// ErrNone means no Chromium-based browser was found.
var ErrNone = errors.New("没找到 Chrome、Edge、Brave 或 Chromium")

var macApps = []string{"Google Chrome", "Microsoft Edge", "Brave Browser", "Chromium"}

var linuxBins = []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "microsoft-edge", "brave-browser"}

// Open shows url in the account's own browser, creating that browser user on
// first use. A running one gets the URL as a new tab and keeps the proxy it
// started with. proxy is the account's egress; "" means the system's.
func Open(id, url, proxy string) error {
	dir := core.BrowserDir(id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	flags := []string{"--user-data-dir=" + dir, "--no-first-run", "--no-default-browser-check"}
	if proxy != "" {
		flags = append(flags, "--proxy-server="+proxy)
	}
	flags = append(flags, url)
	switch runtime.GOOS {
	case "darwin":
		app, ok := macApp()
		if !ok {
			return ErrNone
		}
		return exec.Command("open", append([]string{"-na", app, "--args"}, flags...)...).Run()
	case "windows":
		bin, ok := windowsBin()
		if !ok {
			return ErrNone
		}
		return exec.Command(bin, flags...).Start()
	default:
		for _, name := range linuxBins {
			if p, err := exec.LookPath(name); err == nil {
				return exec.Command(p, flags...).Start()
			}
		}
		return ErrNone
	}
}

// Available reports whether Open has a browser to use.
func Available() bool {
	switch runtime.GOOS {
	case "darwin":
		_, ok := macApp()
		return ok
	case "windows":
		_, ok := windowsBin()
		return ok
	default:
		for _, name := range linuxBins {
			if _, err := exec.LookPath(name); err == nil {
				return true
			}
		}
		return false
	}
}

// Remove deletes the account's browser user, and with it its web login.
func Remove(id string) { _ = os.RemoveAll(core.BrowserDir(id)) }

func macApp() (string, bool) {
	home, _ := os.UserHomeDir()
	for _, name := range macApps {
		for _, root := range []string{"/Applications", filepath.Join(home, "Applications")} {
			p := filepath.Join(root, name+".app")
			if fi, err := os.Stat(p); err == nil && fi.IsDir() {
				return p, true
			}
		}
	}
	return "", false
}

func windowsBin() (string, bool) {
	var roots []string
	for _, k := range []string{"ProgramFiles", "ProgramFiles(x86)", "LocalAppData"} {
		if v := os.Getenv(k); v != "" {
			roots = append(roots, v)
		}
	}
	for _, rel := range []string{`Google\Chrome\Application\chrome.exe`, `Microsoft\Edge\Application\msedge.exe`, `BraveSoftware\Brave-Browser\Application\brave.exe`} {
		for _, root := range roots {
			p := filepath.Join(root, rel)
			if _, err := os.Stat(p); err == nil {
				return p, true
			}
		}
	}
	return "", false
}
