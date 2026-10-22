//go:build !windows

package main

import (
	"os"
	"os/signal"
	"syscall"
)

func notifyReload() (<-chan os.Signal, func()) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGHUP)
	return sigCh, func() { signal.Stop(sigCh) }
}
