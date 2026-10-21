//go:build linux

package launchguard

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// supervise holds the generation's lifetime lock until every descendant of
// the launch has exited. As the subreaper, every process the launch leaves
// behind is reparented here, so no descendant can outlive the lock. It returns
// the desktop's exit status when the desktop was the last to report one.
func supervise(lifetime, broker *os.File) int {
	defer func() { _ = lifetime.Close() }()
	defer func() { _ = broker.Close() }()
	status := 0
	for {
		var wait unix.WaitStatus
		_, err := unix.Wait4(-1, &wait, 0, nil)
		switch {
		case errors.Is(err, unix.EINTR):
			continue
		case errors.Is(err, unix.ECHILD):
			return status
		case err != nil:
			return 1
		}
		switch {
		case wait.Exited():
			status = wait.ExitStatus()
		case wait.Signaled():
			status = 128 + int(wait.Signal())
		}
	}
}
