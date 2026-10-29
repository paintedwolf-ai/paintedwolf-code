package exec

import (
	"fmt"
	"os/exec"
	"sync"

	"github.com/lycaon/lycaon/internal/confine"
)

// ProcessGroup retains the executor's actual supervisor, native Wait, and tree
// guard for an asynchronous caller. Stop cannot signal a settled invocation.
type ProcessGroup struct {
	command  *exec.Cmd
	guard    runGuard
	cleanup  func()
	waitOnce sync.Once
	done     chan struct{}
	mu       sync.Mutex
	settled  bool
	waitErr  error
}

// StartInOwnGroup registers a prepared invocation's existing tree owner before
// the helper releases payload execution. The caller retains its input cleanup.
func StartInOwnGroup(cmd *exec.Cmd) (*ProcessGroup, error) {
	if _, err := claimPrepared(cmd); err != nil {
		return nil, err
	}
	group, err := prepareProcessGroup(cmd)
	if err != nil {
		return nil, err
	}
	if err := confine.ValidateProcessInputs(group.command); err != nil {
		return nil, group.release(err)
	}
	if err := group.start(); err != nil {
		return nil, err
	}
	if err := confine.ConfirmProcessLaunch(group.command); err != nil {
		return nil, group.abort(err)
	}
	return group, nil
}

func prepareProcessGroup(cmd *exec.Cmd) (*ProcessGroup, error) {
	cmd, cleanup, err := superviseCommand(cmd, func() {}, ProcessPriorityNormal)
	if err != nil {
		return nil, err
	}
	guard, err := newRunGuard(ProcessPriorityNormal)
	if err != nil {
		cleanup()
		return nil, err
	}
	guard.configure(cmd)
	if cmd.WaitDelay == 0 {
		cmd.WaitDelay = PipelineWaitDelay
	}
	return &ProcessGroup{command: cmd, guard: guard, cleanup: cleanup, done: make(chan struct{})}, nil
}

func (g *ProcessGroup) release(err error) error {
	g.guard.release()
	g.cleanup()
	return err
}

func (g *ProcessGroup) abort(err error) error {
	g.guard.kill(g.command)
	_ = g.command.Wait()
	return g.release(fmt.Errorf("process group: %w", err))
}

func (g *ProcessGroup) start() error {
	if err := g.command.Start(); err != nil {
		return g.release(err)
	}
	if err := g.guard.onStarted(g.command); err != nil {
		return g.abort(err)
	}
	return nil
}

// The confinement availability probe already carries its fixed profile inputs;
// it has no native invocation binding to claim or release.
func runHelperProbeGroup(cmd *exec.Cmd) error {
	group, err := prepareProcessGroup(cmd)
	if err != nil {
		return err
	}
	if err := group.start(); err != nil {
		return err
	}
	return group.Wait()
}

// Wait settles native exit and tree cleanup once, including concurrent callers.
func (g *ProcessGroup) Wait() error {
	g.waitOnce.Do(func() {
		g.waitErr = g.command.Wait()
		g.mu.Lock()
		g.settled = true
		g.guard.kill(g.command)
		g.guard.release()
		g.cleanup()
		g.mu.Unlock()
		close(g.done)
	})
	<-g.done
	return g.waitErr
}

// Stop terminates this live group through its existing guard; Wait still owns
// native settlement and release. It never touches a later reused process group.
func (g *ProcessGroup) Stop() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.settled {
		g.guard.kill(g.command)
	}
}

// RunInOwnGroup settles the same owned lifecycle for synchronous producers.
func RunInOwnGroup(cmd *exec.Cmd) error {
	group, err := StartInOwnGroup(cmd)
	if err != nil {
		return err
	}
	return group.Wait()
}
