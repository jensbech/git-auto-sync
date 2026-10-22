//go:build !windows

package main

import "syscall"

func dirWritable(dir string) error {
	return syscall.Access(dir, 0x2)
}
