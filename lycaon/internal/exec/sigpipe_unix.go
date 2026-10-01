//go:build unix

package exec

import (
	"errors"
	"os/exec"
	"syscall"
)

// killedBySIGPIPE reports whether a stage's wait error is a SIGPIPE death — the
// producer wrote into a pipe whose consumer already exited.
func killedBySIGPIPE(err error) bool {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return false
	}
	ws, ok := exitErr.Sys().(syscall.WaitStatus)
	return ok && ws.Signaled() && ws.Signal() == syscall.SIGPIPE
}
