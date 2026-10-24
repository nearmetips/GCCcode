// Package notify shows a local notification without extra dependencies.
package notify

import (
	"os/exec"
	"runtime"
	"strings"
)

// Send displays title and body. A missing notifier is ignored.
func Send(title, body string) {
	title = oneLine(title)
	body = oneLine(body)
	switch runtime.GOOS {
	case "darwin":
		script := `display notification ` + quote(body) + ` with title ` + quote(title)
		_ = exec.Command("osascript", "-e", script).Start()
	case "linux":
		if path, err := exec.LookPath("notify-send"); err == nil {
			_ = exec.Command(path, title, body).Start()
		}
	}
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len([]rune(s)) > 180 {
		s = string([]rune(s)[:180])
	}
	return s
}

func quote(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}
