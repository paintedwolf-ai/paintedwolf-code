// Command userlands builds, verifies and records the pinned reference userland
// images, and is the driver each of their containers runs.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/lycaon/lycaon/test/referenceuserland"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return referenceuserland.Command(ctx, args, os.Stdin, os.Stdout, os.Stderr)
}
