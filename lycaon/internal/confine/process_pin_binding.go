package confine

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/lycaon/lycaon/internal/osprocess"
)

const (
	pinCwdFDFlag         = "--pin-cwd-fd"
	pinReadyFDFlag       = "--pin-ready-fd"
	pinEntryFlag         = "--pin-entry"
	pinHostExecutionFlag = "--pin-host-execution"
	pinExecutableFDFlag  = "--pin-executable-fd"
	pinFinalFlag         = "--pin-final"
)

type pendingProcessPins struct {
	ctx                    context.Context
	pins                   *ProcessPins
	read, write            *os.File
	arguments, environment []string
	executable, cwd        *os.File
	security               osprocess.ProcessSecurity
	maximumBytes           int
	confined               bool
	frozen                 bool
	closeNative            func()
}

var processPinStarts sync.Map

func attachPinnedLaunch(ctx context.Context, cmd *exec.Cmd, p *ProcessPins, name string, args []string, confined bool) (func(), error) {
	if err := pinnedPlatformAvailable(p); err != nil {
		return nil, err
	}
	if err := p.revalidate(ctx, name); err != nil {
		return nil, err
	}
	security, err := osprocess.ProcessCredentials(os.Getpid())
	if err != nil {
		return nil, err
	}
	maximum, err := nativeArgumentBudget()
	if err != nil {
		return nil, err
	}
	read, write, err := newPinnedTransport()
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	if p.closed || p.bound {
		p.mu.Unlock()
		_ = read.Close()
		_ = write.Close()
		return nil, ErrLaunchIdentityChanged
	}
	p.bound = true
	state := &pendingProcessPins{ctx: ctx, pins: p, read: read, write: write, executable: p.images[len(p.images)-1].file, cwd: p.cwd, security: security, maximumBytes: maximum, confined: confined}
	if err := p.limits.checkCommand(name, args, nil); err != nil {
		p.mu.Unlock()
		_ = read.Close()
		_ = write.Close()
		return nil, err
	}
	state.arguments = cloneVector(append([]string{name}, args...))
	cwdFD := 3 + len(cmd.ExtraFiles)
	readyFD := cwdFD + 1
	cmd.ExtraFiles = append(cmd.ExtraFiles, p.cwd, write)
	entry := p.images[0].path
	platformFlags, err := attachPlatformPins(cmd, state)
	p.mu.Unlock()
	if err != nil {
		_ = read.Close()
		_ = write.Close()
		return nil, err
	}
	split := slices.Index(cmd.Args, "--")
	if split < 0 {
		_ = read.Close()
		_ = write.Close()
		return nil, fmt.Errorf("pinned helper has no target boundary")
	}
	flags := []string{pinCwdFDFlag, strconv.Itoa(cwdFD), pinReadyFDFlag, strconv.Itoa(readyFD), pinEntryFlag, entry}
	flags = append(flags, platformFlags...)
	cmd.Args = append(append(append([]string{}, cmd.Args[:split]...), flags...), cmd.Args[split:]...)
	processPinStarts.Store(cmd, state)
	return func() {
		processPinStarts.Delete(cmd)
		_ = read.Close()
		_ = write.Close()
		if state.closeNative != nil {
			state.closeNative()
		}
	}, nil
}

// FreezePinnedLaunch snapshots the final sanitized child environment and admits
// both model payload and native OS vector bounds before helper startup.
func FreezePinnedLaunch(cmd *exec.Cmd) error {
	value, ok := processPinStarts.Load(cmd)
	if !ok {
		return nil
	}
	state := value.(*pendingProcessPins)
	if state.frozen {
		return fmt.Errorf("pinned launch vectors are already frozen")
	}
	if cmd.Env == nil {
		cmd.Env = os.Environ()
	}
	if err := state.pins.limits.checkCommand(state.arguments[0], state.arguments[1:], cmd.Env); err != nil {
		return err
	}
	var bytes int64
	for _, vector := range [][]string{state.arguments, cmd.Env} {
		for _, word := range vector {
			bytes += int64(len(word)) + 1
		}
	}
	bytes += int64(len(state.arguments)+len(cmd.Env)+2) * int64(unsafe.Sizeof(uintptr(0)))
	if bytes > int64(state.maximumBytes) {
		return &PlatformLaunchLimitError{int64(state.maximumBytes), bytes}
	}
	state.environment = cloneVector(cmd.Env)
	state.frozen = true
	// The helper enters the held directory after applying confinement. Starting
	// it by a mutable cwd pathname would defeat the descriptor handoff.
	cmd.Dir = ""
	return nil
}

func cloneVector(vector []string) []string {
	out := make([]string, len(vector))
	for i, value := range vector {
		out[i] = strings.Clone(value)
	}
	return out
}

// ConfirmPinnedLaunch is called by the existing process guard after registration.
// Failure leaves release to that guard's owned kill/reap path.
func ConfirmPinnedLaunch(cmd *exec.Cmd) error {
	value, ok := processPinStarts.Load(cmd)
	if !ok {
		return nil
	}
	state := value.(*pendingProcessPins)
	if !state.frozen || cmd.Process == nil {
		return fmt.Errorf("pinned launch was not frozen before start")
	}
	ctx, cancel := context.WithTimeout(state.ctx, 10*time.Second)
	defer cancel()
	_ = state.write.Close()
	if handled, err := confirmPlatformPins(ctx, cmd, state); handled {
		return err
	}
	type readyResult struct {
		bytes []byte
		err   error
	}
	ready := make(chan readyResult, 1)
	go func() { data, err := io.ReadAll(io.LimitReader(state.read, 16)); ready <- readyResult{data, err} }()
	select {
	case <-ctx.Done():
		_ = state.read.Close()
		return ctx.Err()
	case result := <-ready:
		if result.err != nil {
			return result.err
		}
		if len(result.bytes) == 6 && result.bytes[0] == 1 && result.bytes[1] == 2 {
			return fmt.Errorf("pinned helper exec: %w", syscall.Errno(binary.LittleEndian.Uint32(result.bytes[2:])))
		}
		if len(result.bytes) != 1 || result.bytes[0] != 1 {
			return osprocess.ErrExecutableObservationUnavailable
		}
	}
	if err := awaitPinnedStop(ctx, cmd.Process.Pid); err != nil {
		return err
	}
	observed, err := osprocess.ObserveStoppedLaunch(ctx, cmd.Process.Pid, state.executable, state.cwd, state.maximumBytes)
	if err != nil {
		return err
	}
	expected, err := state.pins.expectedArguments(state.arguments)
	if err != nil {
		return err
	}
	if !observed.ExecutableMatches || !observed.DirectoryMatches || !slices.Equal(observed.Arguments, expected) {
		return ErrLaunchIdentityChanged
	}
	if observed.EnvironmentObserved && !slices.Equal(observed.Environment, state.environment) {
		return ErrLaunchIdentityChanged
	}
	if !state.pins.permitsSecurity(state.security, observed.Security, state.confined) {
		return ErrLaunchIdentityChanged
	}
	if err := state.pins.revalidate(ctx, state.arguments[0]); err != nil {
		return err
	}
	return resumePinnedProcess(cmd.Process)
}
