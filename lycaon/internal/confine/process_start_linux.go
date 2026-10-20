//go:build linux

package confine

import (
	"context"
	"os"
	"os/exec"
	"strconv"

	"github.com/lycaon/lycaon/internal/osprocess"
	"golang.org/x/sys/unix"
)

// Required pins select the held ELF mechanism before admission. Scripts never
// silently become descriptor launches; their stronger path barrier is unavailable.
func pinnedPlatformAvailable(p *ProcessPins) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || platformLaunchMechanism(p) != HeldBinaryLaunch {
		return osprocess.ErrExecutableObservationUnavailable
	}
	_, err := heldExecutablePath(int(p.images[len(p.images)-1].file.Fd()))
	return err
}
func awaitPinnedStop(context.Context, int) error {
	return osprocess.ErrExecutableObservationUnavailable
}
func resumePinnedProcess(*os.Process) error { return osprocess.ErrExecutableObservationUnavailable }

// Linux derives the combined argv/environment allowance from the inherited soft
// stack limit, with its32-page floor and three-quarter8MiB architecture cap.
// Kernel execution still decides actual admissibility, including per-string and
// pointer overhead; this preflight is not successful native qualification.
func nativeArgumentBudget() (int, error) {
	var stack unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_STACK, &stack); err != nil {
		return 0, err
	}
	return linuxArgumentBudget(stack.Cur, os.Getpagesize()), nil
}
func linuxArgumentBudget(stack uint64, pageBytes int) int {
	return max(32*pageBytes, int(min(stack/4, uint64(6<<20))))
}

func newPinnedTransport() (*os.File, *os.File, error) { return nativeGatePair() }
func attachPlatformPins(cmd *exec.Cmd, state *pendingProcessPins) ([]string, error) {
	fd := 3 + len(cmd.ExtraFiles)
	cmd.ExtraFiles = append(cmd.ExtraFiles, state.executable)
	return []string{pinExecutableFDFlag, strconv.Itoa(fd)}, nil
}
func confirmPlatformPins(ctx context.Context, _ *exec.Cmd, state *pendingProcessPins) (bool, error) {
	return true, confirmNativeGate(ctx, state)
}
func platformLaunchMechanism(p *ProcessPins) LaunchMechanism {
	if p.nativeELF {
		return HeldBinaryLaunch
	}
	return UnsupportedLaunch
}
