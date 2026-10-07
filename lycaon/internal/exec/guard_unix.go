//go:build unix

package exec

import (
	"errors"
	"os/exec"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/lycaon/lycaon/internal/osprocess"
)

// DefaultBelowNormalNice is the nice value applied for ProcessPriorityBelowNormal.
const DefaultBelowNormalNice = 5

type unixGuard struct {
	belowNormal bool
	// pgid is captured before the leader can be reaped.
	pgid atomic.Int64
	// untrack releases the group from the reaper that kills it if the engine dies.
	untrack func()

	mu sync.Mutex
	// terminated marks a tree that was asked to exit; release then sweeps
	// whatever outlived the leader.
	terminated bool
	// killTimer is the SIGKILL fallback armed by terminate.
	killTimer *time.Timer
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
	// Cancellation terminates the entire session the leader started.
	cmd.Cancel = func() error {
		g.terminate(cmd)
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
	// Below normal never raises priority: a child that inherited a higher nice
	// keeps it, and lowering a nice value needs privilege.
	nice, err := processNice(cmd.Process.Pid)
	if err == nil && nice < DefaultBelowNormalNice {
		err = syscall.Setpriority(syscall.PRIO_PROCESS, cmd.Process.Pid, DefaultBelowNormalNice)
	}
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

// terminate sends SIGTERM to the tree and arms a SIGKILL for whatever is still
// running after TerminateGrace. A second call is a no-op.
func (g *unixGuard) terminate(cmd *exec.Cmd) {
	g.mu.Lock()
	if g.terminated {
		g.mu.Unlock()
		return
	}
	g.terminated = true
	g.mu.Unlock()
	if !g.signal(cmd, syscall.SIGTERM) {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.killTimer = time.AfterFunc(TerminateGrace, func() { g.kill(cmd) })
}

func (g *unixGuard) kill(cmd *exec.Cmd) {
	g.mu.Lock()
	g.terminated = true
	if g.killTimer != nil {
		g.killTimer.Stop()
		g.killTimer = nil
	}
	g.mu.Unlock()
	g.signal(cmd, syscall.SIGKILL)
}

// signal delivers sig to the leader's process group and then to every process
// still in the session the leader started: a shell with job control moves each
// job into its own group, and only the session ties those back to the command.
// Without a recorded group it falls back to the leader alone.
func (g *unixGuard) signal(cmd *exec.Cmd, sig syscall.Signal) bool {
	pgid := int(g.pgid.Load())
	if pgid <= 0 {
		if cmd == nil || cmd.Process == nil {
			return false
		}
		_ = cmd.Process.Signal(sig)
		return true
	}
	_ = syscall.Kill(-pgid, sig)
	for _, pid := range osprocess.SessionMembers(pgid) {
		_ = syscall.Kill(pid, sig)
	}
	return true
}

// release runs after the leader is reaped. A terminated tree gets one final
// sweep so nothing that outlived the leader survives the command.
func (g *unixGuard) release() {
	g.mu.Lock()
	terminated := g.terminated
	timer := g.killTimer
	g.killTimer = nil
	g.mu.Unlock()
	if timer != nil {
		timer.Stop()
	}
	if terminated {
		g.signal(nil, syscall.SIGKILL)
	}
	if g.untrack != nil {
		g.untrack()
	}
}
