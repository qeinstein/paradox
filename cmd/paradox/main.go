// Command paradox is the PARADOX runtime, evaluator, and local dashboard.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/qeinstein/paradox/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(cli.Run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}
