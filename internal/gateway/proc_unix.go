//go:build !windows

package gateway

import (
	"os"
	"os/exec"
	"syscall"
)

// detach puts the child in its own session so closing the terminal does not
// take the gateway down with it.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

func terminate(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Signal(syscall.SIGTERM)
}
