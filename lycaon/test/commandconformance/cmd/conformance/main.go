// Command conformance captures the reference snapshots of the command conformance corpus.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/lycaon/lycaon/test/commandconformance"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return commandconformance.Command(ctx, args, os.Stdout, os.Stderr)
}
