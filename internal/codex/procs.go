package codex

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// listProcesses is replaced in tests so a running desktop app does not block them.
var listProcesses = Running

// Running lists live Codex processes: the CLI, the Codex app and the Codex
// helpers the ChatGPT desktop app runs. Any of them may rewrite auth.json.
func Running() []string {
	if runtime.GOOS == "windows" {
		return runningWindows()
	}
	out, err := exec.Command("ps", "-axo", "pid=,comm=").Output()
	if err != nil {
		return nil
	}
	self := os.Getpid()
	parent := os.Getppid()
	var found []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		sp := strings.IndexByte(line, ' ')
		if sp < 0 {
			continue
		}
		pid, err := strconv.Atoi(line[:sp])
		if err != nil || pid == self || pid == parent {
			continue
		}
		comm := strings.TrimSpace(line[sp+1:])
		if isCodexExe(comm) {
			found = append(found, strconv.Itoa(pid)+"  "+comm)
		}
	}
	return found
}

func isCodexExe(comm string) bool {
	base := strings.ToLower(filepath.Base(comm))
	return base == "codex" || base == "codex.exe" || strings.HasPrefix(base, "codex (") || base == "codex-app-server"
}

func runningWindows() []string {
	out, err := exec.Command("tasklist", "/FO", "CSV", "/NH").Output()
	if err != nil {
		return nil
	}
	var found []string
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Split(line, `","`)
		if len(fields) < 2 {
			continue
		}
		name := strings.Trim(fields[0], `"`)
		if isCodexExe(name) {
			found = append(found, strings.Trim(fields[1], `"`)+"  "+name)
		}
	}
	return found
}
