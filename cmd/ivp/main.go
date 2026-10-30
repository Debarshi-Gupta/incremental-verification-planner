package main

import (
	"context"
	"fmt"
	"os"

	"github.com/ivp/ivp/internal/cli"
)

var (
	version = "0.1.0"
	commit  = "unknown"
	builtAt = "unknown"
)

func main() {
	if err := cli.Execute(context.Background(), version, commit, builtAt); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(cli.ExitCode(err))
	}
}
