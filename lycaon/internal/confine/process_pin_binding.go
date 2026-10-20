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
)

const (
	pinCwdFDFlag         = "--launch-cwd-fd"
	pinReadyFDFlag       = "--launch-ready-fd"
	pinReleaseFDFlag     = "--launch-release-fd"
	pinExecutableFDFlag  = "--launch-entry-fd"
	pinEntryFlag         = "--launch-entry"
	pinHostExecutionFlag = "--launch-host-execution"
	pinFinalFlag         = "--launch-final"
)

type processHandoff struct {
	ctx                                              context.Context
	inputs                                           *ProcessInputs
	readyRead, readyWrite, releaseRead, releaseWrite *os.File
	environment, helperArguments                     []string
	check                                            func(context.Context) error
}

var processHandoffs sync.Map

// BindProcessInputs installs the same native-path handoff for every prepared
// LaunchPlan. The authority callback is the initiating owner's current check.
func BindProcessInputs(ctx context.Context, cmd *exec.Cmd, inputs *ProcessInputs, name string, args, environment []string, check func(context.Context) error) (func(), error) {
	if inputs == nil || check == nil {
		return nil, fmt.Errorf("launch requires captured inputs and current authority")
	}
	if err := inputs.limits.checkCommand(name, args, environment); err != nil {
		return nil, err
	}
	if err := inputs.revalidate(ctx); err != nil {
		return nil, err
	}
	maximum, err := nativeArgumentBudget()
	if err != nil {
		return nil, err
	}
	readyRead, readyWrite, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	releaseRead, releaseWrite, err := os.Pipe()
	if err != nil {
		_ = readyRead.Close()
		_ = readyWrite.Close()
		return nil, err
	}
	state := &processHandoff{ctx: ctx, inputs: inputs, readyRead: readyRead, readyWrite: readyWrite, releaseRead: releaseRead, releaseWrite: releaseWrite, check: check}
	closeState := func() {
		_ = readyRead.Close()
		_ = readyWrite.Close()
		_ = releaseRead.Close()
		_ = releaseWrite.Close()
	}
	inputs.mu.Lock()
	if inputs.closed || inputs.bound {
		inputs.mu.Unlock()
		closeState()
		return nil, ErrLaunchIdentityChanged
	}
	inputs.bound = true
	first := 3 + len(cmd.ExtraFiles)
	cmd.ExtraFiles = append(cmd.ExtraFiles, inputs.cwd, readyWrite, releaseRead, inputs.entry.file)
	entry := inputs.entry.path
	inputs.mu.Unlock()
	split := slices.Index(cmd.Args, "--")
	if split < 0 {
		closeState()
		return nil, fmt.Errorf("native helper target boundary absent")
	}
	flags := []string{pinCwdFDFlag, strconv.Itoa(first), pinReadyFDFlag, strconv.Itoa(first + 1), pinReleaseFDFlag, strconv.Itoa(first + 2), pinExecutableFDFlag, strconv.Itoa(first + 3), pinEntryFlag, entry}
	cmd.Args = append(append(append([]string(nil), cmd.Args[:split]...), flags...), cmd.Args[split:]...)
	cmd.Env = cloneVector(environment)
	state.environment = cloneVector(environment)
	state.helperArguments = cloneVector(cmd.Args)
	cmd.Dir = ""
	// The helper envelope and the eventual native invocation each obey the actual
	// combined native argv/environment allowance; neither ceiling is an OS promise.
	for _, vector := range [][]string{append([]string{name}, args...), cmd.Args} {
		var bytes int64
		for _, group := range [][]string{vector, environment} {
			for _, word := range group {
				bytes += int64(len(word)) + 1
			}
		}
		bytes += int64(len(vector)+len(environment)+2) * int64(unsafe.Sizeof(uintptr(0)))
		if bytes > int64(maximum) {
			closeState()
			return nil, &PlatformLaunchLimitError{int64(maximum), bytes}
		}
	}
	processHandoffs.Store(cmd, state)
	return func() { processHandoffs.Delete(cmd); closeState() }, nil
}

func cloneVector(vector []string) []string {
	out := make([]string, len(vector))
	for i, value := range vector {
		out[i] = strings.Clone(value)
	}
	return out
}

// ValidateProcessInputs runs before starting a prepared command, including after
// the existing Linux supervisor adds its own prefix. Caller I/O setup is allowed;
// changing the captured executable vector/environment/cwd is not.
func ValidateProcessInputs(cmd *exec.Cmd) error {
	value, ok := processHandoffs.Load(cmd)
	if !ok {
		return fmt.Errorf("command lacks prepared launch inputs")
	}
	state := value.(*processHandoff)
	count := len(state.helperArguments)
	if len(cmd.Args) < count || !slices.Equal(cmd.Args[len(cmd.Args)-count:], state.helperArguments) || !slices.Equal(cmd.Env, state.environment) || cmd.Dir != "" {
		return ErrLaunchIdentityChanged
	}
	if err := state.check(state.ctx); err != nil {
		return err
	}
	return state.inputs.revalidate(state.ctx)
}

// ConfirmProcessLaunch is called only after the existing owner registered the
// actual process/tree. It releases immutable inputs once; failure stays with that
// owner's native kill/reap and terminal settlement rather than replaying a launch.
func ConfirmProcessLaunch(cmd *exec.Cmd) error {
	value, ok := processHandoffs.Load(cmd)
	if !ok {
		return nil
	}
	state := value.(*processHandoff)
	ctx, cancel := context.WithTimeout(state.ctx, 10*time.Second)
	defer cancel()
	_ = state.readyWrite.Close()
	_ = state.releaseRead.Close()
	ready, err := readLaunchRecord(ctx, state.readyRead, 1)
	if err != nil {
		return err
	}
	if len(ready) != 1 || ready[0] != 1 {
		return fmt.Errorf("native helper did not reach launch handoff")
	}
	if err := state.check(ctx); err != nil {
		return err
	}
	if err := state.inputs.revalidate(ctx); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := state.releaseWrite.Write([]byte{1}); err != nil {
		return err
	}
	_ = state.releaseWrite.Close()
	record, err := readLaunchRecord(ctx, state.readyRead, 16)
	if err != nil {
		return err
	}
	if len(record) == 5 && record[0] == 2 {
		return fmt.Errorf("native exec: %w", syscall.Errno(binary.LittleEndian.Uint32(record[1:])))
	}
	if len(record) != 0 {
		return fmt.Errorf("invalid native exec handoff record")
	}
	return nil // CLOEXEC only, never a claim of completed output or effects.
}

func readLaunchRecord(ctx context.Context, file *os.File, maximum int) ([]byte, error) {
	type result struct {
		bytes []byte
		err   error
	}
	done := make(chan result, 1)
	go func() {
		if maximum == 1 {
			var b [1]byte
			_, err := io.ReadFull(file, b[:])
			done <- result{b[:], err}
			return
		}
		bytes, err := io.ReadAll(io.LimitReader(file, int64(maximum+1)))
		done <- result{bytes, err}
	}()
	select {
	case <-ctx.Done():
		_ = file.Close()
		return nil, ctx.Err()
	case out := <-done:
		return out.bytes, out.err
	}
}
