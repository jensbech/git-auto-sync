//go:build windows

package common

import (
	"os"
	"syscall"
)

func gitSysProcAttr() *syscall.SysProcAttr {
	return nil
}

func terminateGit(p *os.Process) error {
	return p.Kill()
}
