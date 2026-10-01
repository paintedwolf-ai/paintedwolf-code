//go:build windows

package exec

import (
	"fmt"
	"io"
	"os"
)

// StartReaper is unnecessary on Windows: each guarded command runs in a job
// object that terminates when the engine's last handle to it closes, which
// process exit does.
func StartReaper(string, ...string) error { return nil }

func track(byte, int) func() { return func() {} }

// RunReaper exists only so the shared entrypoint builds; StartReaper never launches it here.
func RunReaper(io.Reader) int {
	fmt.Fprintln(os.Stderr, "the process reaper is not used on Windows")
	return 2
}
