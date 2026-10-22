//go:build !windows

package common

import (
	"os"
	"syscall"
)

func gitSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

func terminateGit(p *os.Process) error {
	if err := syscall.Kill(-p.Pid, syscall.SIGTERM); err != nil {
		return p.Kill()
	}
	return nil
}
