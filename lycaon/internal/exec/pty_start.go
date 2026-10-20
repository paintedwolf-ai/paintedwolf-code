package exec

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/confine"
)

// StartPTY starts a caller-managed pseudo-terminal subprocess.
func StartPTY(ctx context.Context, name string, args []string, opts PTYOpts) (PTY, error) {
	if err := opts.Launch.validate(); err != nil {
		return nil, err
	}
	if err := validatePTYCommand(name); err != nil {
		return nil, err
	}
	size := normalizeWinSize(opts.WinSize)

	execOpts := opts.toExecOpts()
	guard, err := newRunGuard(execOpts.ProcessPriority)
	if err != nil {
		return nil, err
	}

	cmd, cleanup, err := prepareNativeCommand(ctx, name, args, execOpts, nil, true)
	if err != nil {
		guard.release()
		return nil, err
	}
	cmd, cleanup, err = superviseCommand(cmd, cleanup, execOpts.ProcessPriority)
	if err != nil {
		guard.release()
		return nil, err
	}
	if err := confine.ValidateProcessInputs(cmd); err != nil {
		cleanup()
		guard.release()
		return nil, err
	}

	// The controlling terminal also creates the child process group.

	main, err := startWithPTY(cmd, size)
	if err != nil {
		cleanup()
		guard.release()
		return nil, err
	}
	if err := guard.onStarted(cmd); err != nil {
		_ = main.Close()
		guard.kill(cmd)
		// Reap the child after guard setup fails.
		_ = cmd.Wait()
		cleanup()
		guard.release()
		return nil, fmt.Errorf("pty guard onStarted: %w", err)
	}

	return &ptySession{
		main:    main,
		cmd:     cmd,
		guard:   guard,
		cleanup: cleanup,
	}, nil
}
