//go:build unix

package testutil

import (
	"io"
	"os"
	"os/signal"
	"syscall"
)

// The runner sends SIGUSR1 before its backstop ends a run, then SIGQUIT for the stacks.
func init() {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGUSR1)
	go reportPendingWaits(signals, os.Stderr)
}

// reportPendingWaits never reads os.Stderr: testing.M.Run reassigns it with no
// ordering against a signal. Before any wait registers it writes to initStderr.
func reportPendingWaits(signals <-chan os.Signal, initStderr io.Writer) {
	for range signals {
		writePendingWaits(pendingWaitsOutput(initStderr))
	}
}
