package exec

import (
	"os/exec"
	"time"
)

// TerminateGrace is how long a terminated process tree has to exit on its
// termination signal before the guard kills it outright.
const TerminateGrace = 2 * time.Second

// runGuard configures subprocess isolation and tears down process trees on cancel/timeout.
type runGuard interface {
	// configure prepares an unstarted command and takes over its cancellation,
	// so the command must come from exec.CommandContext — os/exec rejects a
	// Cancel hook on a context-free command at Start.
	configure(*exec.Cmd)
	onStarted(*exec.Cmd) error
	// terminate asks the whole tree to exit and kills whatever remains after
	// TerminateGrace.
	terminate(*exec.Cmd)
	// kill ends the whole tree at once.
	kill(*exec.Cmd)
	release()
}
