package web

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/nearmetips/DailiCode/internal/core"
)

var errNoTerminal = errors.New("没找到可以打开的终端程序")

// openTerminal runs a gcccode command in a new terminal window. Signing in
// needs one: every client's login is an interactive program.
var openTerminal = func(args ...string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	switch runtime.GOOS {
	case "darwin":
		p, err := writeScript(self, args)
		if err != nil {
			return err
		}
		return exec.Command("open", "-a", "Terminal", p).Run()
	case "windows":
		return exec.Command("cmd", append([]string{"/c", "start", "GCC Code", "cmd", "/k", self}, args...)...).Run()
	default:
		hold := `"$0" "$@"; echo; printf '%s' '完成了，按回车关闭这个窗口'; read _`
		for _, t := range [][]string{{"x-terminal-emulator", "-e"}, {"gnome-terminal", "--"}, {"konsole", "-e"}, {"xfce4-terminal", "-x"}, {"xterm", "-e"}} {
			if p, err := exec.LookPath(t[0]); err == nil {
				argv := append(append(t[1:], "sh", "-c", hold, self), args...)
				return exec.Command(p, argv...).Start()
			}
		}
		return errNoTerminal
	}
}

// writeScript writes a .command file, which Terminal runs when opened. The
// window starts a fresh login shell, so the store location is pinned here.
func writeScript(self string, args []string) (string, error) {
	dir := filepath.Join(core.Root(), "tmp")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	p := filepath.Join(dir, fmt.Sprintf("login-%d.command", time.Now().UnixNano()))
	if err := os.WriteFile(p, []byte(script(self, core.Root(), args)), 0o700); err != nil {
		return "", err
	}
	return p, nil
}

func script(self, root string, args []string) string {
	words := []string{quote(self)}
	for _, a := range args {
		words = append(words, quote(a))
	}
	return "#!/bin/sh\nclear\nrm -f \"$0\"\nexport GCCCODE_HOME=" + quote(root) + "\n" +
		strings.Join(words, " ") + "\n" +
		"echo\necho '完成了。可以关掉这个窗口，回到 GCC Code 面板。'\n"
}

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// commandLine is what the user can paste into a terminal when no window opens.
func commandLine(args []string) string { return "gcccode " + strings.Join(args, " ") }
