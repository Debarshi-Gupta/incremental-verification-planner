//go:build windows

package main

import (
	"fmt"
	"os/exec"
	"syscall"
)

// isolateProcess starts the child run in its own process group so the test can
// signal exactly the IVP process group without touching the `go test` console.
func isolateProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

// sendInterrupt delivers a console CTRL+BREAK to the process group of pid.
// Go's runtime maps CTRL+BREAK to os.Interrupt, the same signal as Ctrl+C,
// which is what cmd/ivp/main.go registers through signal.NotifyContext.
// This mirrors the os/signal package's own TestCtrlBreak harness.
func sendInterrupt(pid int) error {
	dll, err := syscall.LoadDLL("kernel32.dll")
	if err != nil {
		return fmt.Errorf("load kernel32.dll: %w", err)
	}
	defer dll.Release()
	proc, err := dll.FindProc("GenerateConsoleCtrlEvent")
	if err != nil {
		return fmt.Errorf("find GenerateConsoleCtrlEvent: %w", err)
	}
	if result, _, callErr := proc.Call(syscall.CTRL_BREAK_EVENT, uintptr(pid)); result == 0 {
		return fmt.Errorf("GenerateConsoleCtrlEvent: %v", callErr)
	}
	return nil
}
