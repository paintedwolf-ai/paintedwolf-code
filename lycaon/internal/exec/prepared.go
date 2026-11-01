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
	"github.com/lycaon/lycaon/internal/installbinding"
)

type preparedLifecycle struct {
	once            sync.Once
	mu              sync.Mutex
	started, closed bool
	cleanup         func()
	untrack         func()
}

var preparedLifecycles sync.Map

// launchEnvironment builds the final child environment and the captured cwd
// a launch with opts selects its program from.
func launchEnvironment(opts ExecOpts, stageEnv map[string]string, terminal bool) (*exec.Cmd, string, error) {
	final := &exec.Cmd{}
	if err := configureStageCmdEnv(final, opts, stageEnv); err != nil {
		return nil, "", err
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
			return nil, "", err
		}
	}
	if !filepath.IsAbs(cwd) {
		parent, err := os.Getwd()
		if err != nil {
			return nil, "", err
		}
		cwd = parent + string(filepath.Separator) + cwd
	}
	return final, cwd, nil
}

// LaunchLookup reports the program a launch with opts would select for name:
// the same final PATH, captured cwd and not-found refusal, without starting
// anything. opts.Dir must be the absolute directory the launch would use.
func LaunchLookup(name string, opts ExecOpts) (string, error) {
	if !filepath.IsAbs(opts.Dir) {
		return "", fmt.Errorf("launch lookup requires the absolute launch directory")
	}
	final, cwd, err := launchEnvironment(opts, nil, false)
	if err != nil {
		return "", err
	}
	return resolveLaunchEntry(name, cwd, final.Env)
}

func prepareNativeCommand(ctx context.Context, name string, args []string, opts ExecOpts, stageEnv map[string]string, terminal bool) (*exec.Cmd, func(), error) {
	final, cwd, err := launchEnvironment(opts, stageEnv, terminal)
	if err != nil {
		return nil, nil, err
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
	if err := installbinding.Verify(metadataPath); err != nil {
		return nil, nil, err
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
	if err := installbinding.Verify(self); err != nil {
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
	boundary.Process = opts.Child
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
func StartPrepared(ctx context.Context, cmd *exec.Cmd) error {
	return StartPreparedWith(ctx, cmd, cmd.Start, TrackProcess)
}

// StartPreparedWith lets a retained transport keep its maintained native start
// algorithm while sharing the same admission, registration and settlement door.
// Neither callback grants authority or selects a fallback execution mechanism.
func StartPreparedWith(ctx context.Context, cmd *exec.Cmd, start func() error, register func(int) func()) error {
	if start == nil || register == nil {
		return fmt.Errorf("native start and owner are required")
	}
	state, err := claimPrepared(cmd)
	if err != nil {
		return err
	}
	if err := confine.ValidateProcessInputs(ctx, cmd); err != nil {
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
	if err := confine.ConfirmProcessLaunch(ctx, cmd); err != nil {
		return fail(fmt.Errorf("native launch handoff: %w", err))
	}
	return nil
}

// RunPrepared settles the same per-process lifecycle; specialized group,
// pipeline and PTY owners retain their existing tree guards instead.
func RunPrepared(ctx context.Context, cmd *exec.Cmd) error {
	if err := StartPrepared(ctx, cmd); err != nil {
		return err
	}
	return cmd.Wait()
}

func claimPrepared(cmd *exec.Cmd) (*preparedLifecycle, error) {
	value, ok := preparedLifecycles.Load(cmd)
	if !ok {
		return nil, fmt.Errorf("process lacks prepared lifecycle")
	}
	state := value.(*preparedLifecycle)
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.started || state.closed {
		return nil, fmt.Errorf("prepared process was already consumed")
	}
	state.started = true
	return state, nil
}
