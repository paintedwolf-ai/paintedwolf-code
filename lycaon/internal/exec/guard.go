package exec

import "os/exec"

// runGuard configures subprocess isolation and tears down process trees on cancel/timeout.
type runGuard interface {
	// configure prepares an unstarted command and takes over its cancellation,
	// so the command must come from exec.CommandContext — os/exec rejects a
	// Cancel hook on a context-free command at Start.
	configure(*exec.Cmd)
	onStarted(*exec.Cmd) error
	kill(*exec.Cmd)
	release()
}
