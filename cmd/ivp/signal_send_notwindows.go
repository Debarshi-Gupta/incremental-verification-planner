//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

// isolateProcess is a no-op on Unix: the terminal SIGINT in this test targets
// the IVP pid directly, and the runner already moves its own children into
// separate process groups (Setpgid).
func isolateProcess(cmd *exec.Cmd) {}

// sendInterrupt sends SIGINT to the IVP process, equivalent to the terminal
// Ctrl+C that signal.NotifyContext in main.go must observe.
func sendInterrupt(pid int) error {
	return syscall.Kill(pid, syscall.SIGINT)
}
