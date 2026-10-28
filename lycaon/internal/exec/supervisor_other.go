//go:build !linux

package exec

import "os/exec"

func superviseCommand(cmd *exec.Cmd, cleanup func(), _ ProcessPriority) (*exec.Cmd, func(), error) {
	if err := ensureReaper(); err != nil {
		cleanup()
		return nil, nil, err
	}
	return cmd, cleanup, nil
}
func commandSupervisor(*exec.Cmd) bool       { return false }
func signalSupervisor(int, int64, bool) bool { return false }

func supervisorStartupError(*exec.Cmd) error { return nil }
