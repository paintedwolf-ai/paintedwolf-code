//go:build unix

package testutil

import (
	"os"
	"os/signal"
	"syscall"
)

// The runner sends SIGUSR1 before its backstop ends a run, then SIGQUIT for the stacks.
func init() {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGUSR1)
	go reportPendingWaits(signals)
}

func reportPendingWaits(signals <-chan os.Signal) {
	for range signals {
		writePendingWaits(os.Stderr)
	}
}
