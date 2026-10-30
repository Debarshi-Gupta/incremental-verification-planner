//go:build windows

package execution

import (
	"os/exec"
	"time"
)

func isWindows() bool              { return true }
func configureProcess(_ *exec.Cmd) {}
func terminateProcess(cmd *exec.Cmd, done <-chan struct{}, grace time.Duration) {
	if cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
	select {
	case <-done:
	case <-time.After(grace):
	}
}
