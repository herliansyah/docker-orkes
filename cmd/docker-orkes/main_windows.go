//go:build windows

package main

import (
	"fmt"
	"os"
	"syscall"
)

const (
	processQueryLimitedInformation = 0x1000
	stillActive                    = 259
)

func killProcess(pid int, sig syscall.Signal) error {
	if sig == 0 {
		// Non-destructive probe to check if process is alive on Windows
		handle, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
		if err != nil {
			return err
		}
		defer syscall.CloseHandle(handle)

		var exitCode uint32
		if err := syscall.GetExitCodeProcess(handle, &exitCode); err != nil {
			return err
		}
		if exitCode == stillActive {
			return nil
		}
		return fmt.Errorf("process %d is not running (exit code: %d)", pid, exitCode)
	}

	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}

func setupDaemonSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
	}
}
