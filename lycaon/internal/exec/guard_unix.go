//go:build unix

package exec

import (
	"errors"
	"os/exec"
	"sync/atomic"
	"syscall"
)

// DefaultBelowNormalNice is the nice value applied for ProcessPriorityBelowNormal.
const DefaultBelowNormalNice = 5

type unixGuard struct {
	belowNormal bool
	// pgid is captured before the leader can be reaped.
	pgid atomic.Int64
	// untrack releases the group from the reaper that kills it if the engine dies.
	untrack func()
}

func newRunGuard(priority ProcessPriority) (runGuard, error) {
	return &unixGuard{belowNormal: priority == ProcessPriorityBelowNormal}, nil
}

func (g *unixGuard) configure(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	// A new session is its own group with no controlling terminal; a bare group
	// under a terminal is a background job that stops on its first terminal read.
	// A session leader cannot also Setpgid.
	cmd.SysProcAttr.Setsid = true
	cmd.SysProcAttr.Setpgid = false
	// Cancellation terminates the entire process group.
	cmd.Cancel = func() error {
		g.kill(cmd)
		return nil
	}
}

func (g *unixGuard) onStarted(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	g.recordProcessGroup(cmd.Process.Pid)
	if !g.belowNormal {
		return nil
	}
	err := syscall.Setpriority(syscall.PRIO_PROCESS, cmd.Process.Pid, DefaultBelowNormalNice)
	if errors.Is(err, syscall.ESRCH) {
		// Fast children may exit before priority assignment.
		return nil
	}
	return err
}

// recordProcessGroup stores only child process groups.
func (g *unixGuard) recordProcessGroup(pid int) {
	pgid, err := syscall.Getpgid(pid)
	if err != nil || pgid != pid {
		return
	}
	g.pgid.Store(int64(pgid))
	g.untrack = TrackProcessGroup(pgid)
}

func (g *unixGuard) kill(cmd *exec.Cmd) {
	if pgid := g.pgid.Load(); pgid > 0 {
		_ = syscall.Kill(-int(pgid), syscall.SIGKILL)
		return
	}
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
}

func (g *unixGuard) release() {
	if g.untrack != nil {
		g.untrack()
	}
}
