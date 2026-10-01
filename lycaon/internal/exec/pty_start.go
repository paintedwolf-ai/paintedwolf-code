package exec

import (
	"context"
	"fmt"
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

	cmd, cleanup, err := buildExecCmd(ctx, name, args, execOpts)
	if err != nil {
		guard.release()
		return nil, err
	}
	if err := configureCmdEnv(cmd, execOpts); err != nil {
		cleanup()
		guard.release()
		return nil, err
	}
	cmd.Env = applyDefaultTERM(cmd.Env, opts.Env, opts.InlineEnv)

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
