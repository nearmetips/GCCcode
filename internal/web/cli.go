package web

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// cliView says whether the `gcccode` command reaches this build from a
// terminal. The desktop app offers to install it when it does not.
type cliView struct {
	Installed bool   `json:"installed"`
	Path      string `json:"path,omitempty"`
	Target    string `json:"target"`
	Shell     string `json:"shell,omitempty"`
}

// userBin is where the app links the command. It is on PATH for Homebrew
// and pipx users already; cliInstall adds it for the others.
func userBin() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "bin")
}

func cliStatus() cliView {
	self, _ := os.Executable()
	self, _ = filepath.EvalSymlinks(self)
	v := cliView{Target: filepath.Join(userBin(), "gcccode"), Shell: filepath.Base(os.Getenv("SHELL"))}
	candidates := []string{}
	if p, err := exec.LookPath("gcccode"); err == nil {
		candidates = append(candidates, p)
	}
	home, _ := os.UserHomeDir()
	candidates = append(candidates, v.Target, "/usr/local/bin/gcccode", "/opt/homebrew/bin/gcccode", filepath.Join(home, "go", "bin", "gcccode"))
	for _, p := range candidates {
		real, err := filepath.EvalSymlinks(p)
		if err != nil {
			continue
		}
		if real == self {
			v.Installed, v.Path = true, p
			return v
		}
	}
	return v
}

// handleCLI links this executable as `gcccode` in ~/.local/bin and puts that
// directory on PATH for new shells.
func handleCLI(w http.ResponseWriter, r *http.Request) {
	var body struct{}
	if !decode(w, r, &body) {
		return
	}
	if runtime.GOOS == "windows" {
		http.Error(w, "Windows 上请把程序所在目录加进 PATH", http.StatusBadRequest)
		return
	}
	msg, err := cliInstall()
	reply(w)(msg, err)
}

func cliInstall() (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	self, _ = filepath.EvalSymlinks(self)
	dir := userBin()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	link := filepath.Join(dir, "gcccode")
	if fi, err := os.Lstat(link); err == nil {
		if fi.Mode()&os.ModeSymlink == 0 {
			// A copied binary from an earlier install; replace it with a link to this one.
			if err := os.Remove(link); err != nil {
				return "", fmt.Errorf("替换不了 %s：%w", link, err)
			}
		} else {
			_ = os.Remove(link)
		}
	}
	if err := os.Symlink(self, link); err != nil {
		return "", fmt.Errorf("建不了 %s：%w", link, err)
	}
	msg := "命令行工具已装好：" + link
	added, err := ensurePath(dir)
	if err != nil {
		return msg + "。但没能把它加进 PATH：" + err.Error(), nil
	}
	if added != "" {
		msg += "。已把这个目录加进 " + added + "，新开一个终端窗口就能直接敲 gcccode。"
	} else {
		msg += "。新开一个终端窗口就能直接敲 gcccode。"
	}
	return msg, nil
}

// ensurePath adds dir to PATH in the login shell's profile unless a shell
// already sees it. It returns the file it wrote, or "" when nothing changed.
func ensurePath(dir string) (string, error) {
	if shellSees(dir) {
		return "", nil
	}
	home, _ := os.UserHomeDir()
	shell := filepath.Base(os.Getenv("SHELL"))
	var file, line string
	switch shell {
	case "fish":
		file = filepath.Join(home, ".config", "fish", "conf.d", "gcccode.fish")
		line = "fish_add_path " + dir
	case "bash":
		file = filepath.Join(home, ".bash_profile")
		line = `export PATH="$HOME/.local/bin:$PATH"`
	default:
		file = filepath.Join(home, ".zprofile")
		line = `export PATH="$HOME/.local/bin:$PATH"`
	}
	if b, err := os.ReadFile(file); err == nil && strings.Contains(string(b), ".local/bin") {
		return "", nil
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return "", err
	}
	f, err := os.OpenFile(file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := fmt.Fprintf(f, "\n# GCC Code 命令行工具\n%s\n", line); err != nil {
		return "", err
	}
	return filepath.Base(file), nil
}

// shellSees asks the login shell whether dir is already on its PATH.
var shellSees = func(dir string) bool {
	shell := os.Getenv("SHELL")
	if shell == "" {
		return false
	}
	out, err := exec.Command(shell, "-lc", "echo \"$PATH\"").Output()
	if err != nil {
		return false
	}
	for _, p := range strings.Split(strings.TrimSpace(string(out)), ":") {
		if p == dir {
			return true
		}
	}
	return false
}
