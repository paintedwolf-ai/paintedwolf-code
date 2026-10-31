package exec

import (
	"fmt"
	"os/exec"
	"sync"
)

// StartInOwnGroup starts a command from PrepareCommand in a new session without
// a controlling terminal (a job object on Windows). The whole session dies when
// kill is called, when wait returns, on cancellation, or with the engine.
func StartInOwnGroup(cmd *exec.Cmd) (func(), func() error, error) {
	cmd, cleanup, err := superviseCommand(cmd, func() {}, ProcessPriorityNormal)
	if err != nil {
		return nil, nil, err
	}
	guard, err := newRunGuard(ProcessPriorityNormal)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	guard.configure(cmd)
	if err := cmd.Start(); err != nil {
		guard.release()
		cleanup()
		return nil, nil, err
	}
	if err := guard.onStarted(cmd); err != nil {
		guard.kill(cmd)
		_ = guard.wait(cmd)
		guard.release()
		cleanup()
		return nil, nil, fmt.Errorf("process group: %w", err)
	}
	var once sync.Once
	var waitErr error
	wait := func() error {
		once.Do(func() {
			waitErr = guard.wait(cmd)
			guard.kill(cmd)
			guard.release()
			cleanup()
		})
		return waitErr
	}
	return func() { guard.kill(cmd) }, wait, nil
}

// RunInOwnGroup runs a command from PrepareCommand in a new session without a
// controlling terminal (a job object on Windows). The whole session dies on
// cancellation, when the command exits, or with the engine.
func RunInOwnGroup(cmd *exec.Cmd) error {
	_, wait, err := StartInOwnGroup(cmd)
	if err != nil {
		return err
	}
	return wait()
}
