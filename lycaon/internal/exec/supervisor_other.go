//go:build !linux

package exec

import "os/exec"

func superviseCommand(cmd *exec.Cmd, cleanup func()) (*exec.Cmd, func(), error) {
	return cmd, cleanup, nil
}
func commandSupervisor(*exec.Cmd) bool       { return false }
func signalSupervisor(int, int64, bool) bool { return false }

func supervisorStartupError(*exec.Cmd) error { return nil }
