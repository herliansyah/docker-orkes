//go:build !windows

package main

import (
	"syscall"
)

func killProcess(pid int, sig syscall.Signal) error {
	return syscall.Kill(pid, sig)
}

func setupDaemonSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		Setsid: true,
	}
}
