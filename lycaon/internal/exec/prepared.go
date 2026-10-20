package exec

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/confine"
)

type preparedLifecycle struct {
	once            sync.Once
	mu              sync.Mutex
	started, closed bool
	cleanup         func()
	untrack         func()
}

var preparedLifecycles sync.Map

func prepareNativeCommand(ctx context.Context, name string, args []string, opts ExecOpts, stageEnv map[string]string, terminal bool) (*exec.Cmd, func(), error) {
	final := &exec.Cmd{}
	if err := configureStageCmdEnv(final, opts, stageEnv); err != nil {
		return nil, nil, err
	}
	if final.Env == nil {
		final.Env = os.Environ()
	}
	if terminal {
		final.Env = applyDefaultTERM(final.Env, opts.Env, opts.InlineEnv)
	}
	cwd := final.Dir
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return nil, nil, err
		}
	}
	if !filepath.IsAbs(cwd) {
		parent, err := os.Getwd()
		if err != nil {
			return nil, nil, err
		}
		cwd = parent + string(filepath.Separator) + cwd
	}
	paths := opts.Launch.PathAuthority
	if paths == nil {
		if opts.Launch.Kind == LaunchAgentCommand && opts.Launch.CurrentAuthority == nil {
			return nil, nil, fmt.Errorf("agent launch requires current execution authority or an explicit path authority")
		}
		paths = confine.NativeLaunchPaths(cwd)
	} else if opts.Dir == "" {
		held, err := paths.OpenCwd(ctx)
		if err != nil {
			return nil, nil, err
		}
		if held == nil {
			return nil, nil, fmt.Errorf("cwd authority returned no metadata handle")
		}
		if filepath.IsAbs(held.Name()) {
			cwd = held.Name()
		}
		_ = held.Close()
	}
	selected, err := resolveLaunchEntry(name, cwd, final.Env)
	if err != nil {
		return nil, nil, err
	}
	limits := opts.Launch.InputLimits
	if limits == (confine.LaunchVectorLimits{}) {
		limits, err = confine.NativeVectorLimits()
		if err != nil {
			return nil, nil, err
		}
	}
	metadataPath := selected
	if !filepath.IsAbs(metadataPath) {
		metadataPath = cwd + string(filepath.Separator) + metadataPath
	}
	inputs, err := confine.CaptureProcessInputs(ctx, metadataPath, paths, limits)
	if err != nil {
		return nil, nil, err
	}
	ownedCtx, cancel := context.WithCancel(ctx)
	ctx = ownedCtx
	self, err := os.Executable()
	if err != nil {
		cancel()
		_ = inputs.Close()
		return nil, nil, err
	}
	// Choose actual enforcement before constructing the native helper. An
	// explicitly unconfined host disposition never removes an inherited boundary.
	boundary := confine.Confinement{HostExecution: true}
	if opts.Launch.Confinement != nil {
		boundary = *opts.Launch.Confinement
	}
	cmd, baseCleanup, err := confine.Command(ctx, self, name, args, boundary)
	if err != nil {
		cancel()
		_ = inputs.Close()
		return nil, nil, err
	}
	check := func(current context.Context) error {
		if err := current.Err(); err != nil {
			return err
		}
		if err := opts.Launch.validate(); err != nil {
			return err
		}
		if opts.Launch.CurrentAuthority != nil {
			return opts.Launch.CurrentAuthority(current)
		}
		return nil
	}
	release, err := confine.BindProcessInputs(ctx, cmd, inputs, selected, name, args, final.Env, check)
	if err != nil {
		cancel()
		baseCleanup()
		_ = inputs.Close()
		return nil, nil, err
	}
	state := &preparedLifecycle{}
	state.cleanup = func() { cancel(); release(); baseCleanup(); _ = inputs.Close() }
	preparedLifecycles.Store(cmd, state)
	cleanup := func() {
		state.once.Do(func() {
			preparedLifecycles.Delete(cmd)
			state.mu.Lock()
			state.closed = true
			untrack := state.untrack
			state.untrack = nil
			state.mu.Unlock()
			if untrack != nil {
				untrack()
			}
			state.cleanup()
		})
	}
	return cmd, cleanup, nil
}

func resolveLaunchEntry(name, cwd string, environment []string) (string, error) {
	if strings.ContainsRune(name, '/') || strings.ContainsRune(name, filepath.Separator) {
		path := name
		if !filepath.IsAbs(path) {
			path = cwd + string(filepath.Separator) + path
		}
		if _, err := exec.LookPath(path); err != nil {
			return "", err
		}
		return name, nil
	}
	var path string
	for _, entry := range environment {
		if value, ok := strings.CutPrefix(entry, "PATH="); ok {
			path = value
		}
	}
	selected, err := LookPathAt(name, path, cwd)
	if err != nil {
		return "", &confine.CommandNotFoundError{Name: name}
	}
	return selected, nil
}

// StartPrepared starts the captured native invocation, registers its existing
// per-process owner, and releases current-authorized inputs. Failure kills and
// reaps this actual child; it never retries through another launch mechanism.
func StartPrepared(cmd *exec.Cmd) error {
	return StartPreparedWith(cmd, cmd.Start, TrackProcess)
}

// StartPreparedWith lets a retained transport keep its maintained native start
// algorithm while sharing the same admission, registration and settlement door.
// Neither callback grants authority or selects a fallback execution mechanism.
func StartPreparedWith(cmd *exec.Cmd, start func() error, register func(int) func()) error {
	value, ok := preparedLifecycles.Load(cmd)
	if !ok || start == nil || register == nil {
		return fmt.Errorf("process lacks prepared lifecycle or native owner")
	}
	state := value.(*preparedLifecycle)
	state.mu.Lock()
	if state.started || state.closed {
		state.mu.Unlock()
		return fmt.Errorf("prepared process was already consumed")
	}
	state.started = true
	state.mu.Unlock()
	if err := confine.ValidateProcessInputs(cmd); err != nil {
		return err
	}
	startErr := start()
	if cmd.Process == nil {
		if startErr != nil {
			return startErr
		}
		return fmt.Errorf("native start returned no process")
	}
	releaseOwner := register(cmd.Process.Pid)
	var untrack func()
	if releaseOwner != nil {
		var once sync.Once
		untrack = func() { once.Do(releaseOwner) }
	}
	state.mu.Lock()
	closed := state.closed
	if !closed {
		state.untrack = untrack
	}
	state.mu.Unlock()
	fail := func(err error) error {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if untrack != nil {
			untrack()
		}
		state.mu.Lock()
		state.untrack = nil
		state.mu.Unlock()
		return err
	}
	if startErr != nil {
		return fail(startErr)
	}
	if closed || untrack == nil {
		return fail(fmt.Errorf("native process ownership unavailable or already released"))
	}
	if err := confine.ConfirmProcessLaunch(cmd); err != nil {
		return fail(fmt.Errorf("native launch handoff: %w", err))
	}
	return nil
}

// RunPrepared settles the same per-process lifecycle; specialized group,
// pipeline and PTY owners retain their existing tree guards instead.
func RunPrepared(cmd *exec.Cmd) error {
	if err := StartPrepared(cmd); err != nil {
		return err
	}
	return cmd.Wait()
}
