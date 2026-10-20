//go:build linux

package confine

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/lycaon/lycaon/internal/osprocess"
	"golang.org/x/sys/unix"
)

func confirmNativeGate(ctx context.Context, state *pendingProcessPins) error {
	ready, err := receiveNativeGateReady(ctx, int(state.read.Fd()))
	if err != nil {
		return err
	}
	// Keep this exact process descriptor until the existing guard has reaped the
	// owned tree and releases its launch resources, including on admission failure.
	state.closeNative = ready.Close
	if ready.ExecutableFD < 3 || ready.ThreadID <= 0 {
		return osprocess.ErrExecutableObservationUnavailable
	}
	selected, err := state.executable.Stat()
	if err != nil {
		return err
	}
	actual, err := os.Stat(filepath.Join("/proc", strconv.Itoa(ready.PID), "fd", strconv.Itoa(ready.ExecutableFD)))
	if err != nil || !os.SameFile(selected, actual) {
		return ErrLaunchIdentityChanged
	}
	directory, err := state.cwd.Stat()
	if err != nil {
		return err
	}
	actualDirectory, err := os.Stat(filepath.Join("/proc", strconv.Itoa(ready.PID), "cwd"))
	if err != nil || !os.SameFile(directory, actualDirectory) {
		return ErrLaunchIdentityChanged
	}
	// This is a trusted helper's pre-exec identity, not post-exec credential proof.
	if ready.Credentials.Uid != uint32(state.security.EffectiveUID) || ready.Credentials.Gid != uint32(state.security.EffectiveGID) {
		return ErrLaunchIdentityChanged
	}
	if err := state.pins.revalidate(ctx, state.arguments[0]); err != nil {
		return err
	}
	if err := verifyGatePidfd(int(ready.Process.Fd()), ready.PID); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := unix.Sendmsg(int(state.read.Fd()), []byte{nativeGateVersion, 3}, nil, nil, unix.MSG_DONTWAIT|unix.MSG_NOSIGNAL); err != nil {
		return err
	}
	if err := awaitNativeExec(ctx, int(state.read.Fd())); err != nil {
		return err
	}
	facts := ProcessLaunchFacts{
		Mechanism: HeldBinaryLaunch, SelectedEntryBound: true, DirectoryBound: true, ArgumentsBound: true, EnvironmentBound: true,
		AuxiliaryExecutableName: "/proc/self/fd/" + strconv.Itoa(ready.ExecutableFD), PID: ready.PID, StartTime: ready.StartTime, Namespaces: ready.Namespaces,
	}
	// Observation is opportunistic and explicitly after release. A short-lived or
	// privileged target can be unavailable; no stopped snapshot is synthesized.
	facts.Security, facts.SecurityError = osprocess.ProcessCredentials(ready.PID)
	facts.SecurityObserved = facts.SecurityError == nil
	if err := verifyGatePidfd(int(ready.Process.Fd()), ready.PID); err != nil {
		facts.Security, facts.SecurityObserved, facts.SecurityError = osprocess.ProcessSecurity{}, false, err
	}
	state.pins.mu.Lock()
	state.pins.launchFacts, state.pins.launchFactsPresent = facts, true
	state.pins.mu.Unlock()
	return nil
}

func awaitNativeExec(ctx context.Context, fd int) error {
	var packet [6]byte
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, _, flags, _, err := unix.Recvmsg(fd, packet[:], nil, unix.MSG_DONTWAIT)
		if err == unix.EAGAIN || err == unix.EINTR {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(2 * time.Millisecond):
			}
			continue
		}
		if err != nil {
			return err
		}
		if flags&unix.MSG_TRUNC != 0 {
			return osprocess.ErrExecutableObservationUnavailable
		}
		if n == 0 {
			return nil
		} // CLOEXEC handoff only; not output/effect completion.
		if n == len(packet) && packet[0] == nativeGateVersion && packet[1] == 2 {
			return fmt.Errorf("pinned binary exec: %w", syscall.Errno(binary.LittleEndian.Uint32(packet[2:])))
		}
		return osprocess.ErrExecutableObservationUnavailable
	}
}
