package exec

import (
	"fmt"
	"os/exec"
)

// RunInOwnGroup runs a command from PrepareCommand in a new session without a
// controlling terminal (a job object on Windows). The whole group dies on
// cancellation, when the command exits, or with the engine.
func RunInOwnGroup(cmd *exec.Cmd) error {
	guard, err := newRunGuard(ProcessPriorityNormal)
	if err != nil {
		return err
	}
	defer guard.release()
	guard.configure(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	if err := guard.onStarted(cmd); err != nil {
		guard.kill(cmd)
		_ = cmd.Wait()
		return fmt.Errorf("process group: %w", err)
	}
	err = cmd.Wait()
	guard.kill(cmd)
	return err
}
