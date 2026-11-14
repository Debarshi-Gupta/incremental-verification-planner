package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/ivp/ivp/internal/cli"
)

var (
	version = "0.1.0"
	commit  = "unknown"
	builtAt = "unknown"
)

func main() {
	// The first interrupt/cancel signal cancels the command context: on
	// Windows the runtime maps both CTRL+C and targeted CTRL+BREAK console
	// events to os.Interrupt. The runner's process-group teardown then marks
	// in-flight commands CANCELLED, the run paths persist the execution
	// report without letting context-bound post-run steps overwrite the
	// status, and StatusCancelled maps to exit code 8 (LLD cancellation
	// contract). stop() is invoked before the error exit so a repeat signal
	// is not swallowed while flushing the report.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := cli.Execute(ctx, version, commit, builtAt); err != nil {
		fmt.Fprintln(os.Stderr, err)
		stop()
		os.Exit(cli.ExitCode(err))
	}
}
