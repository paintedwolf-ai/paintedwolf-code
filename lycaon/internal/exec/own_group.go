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
	cmd, cleanup, err := superviseCommand(cmd, func() {}, ProcessPriorityNormal)
	if err != nil {
		return nil, err
	}
	guard, err := newRunGuard(ProcessPriorityNormal)
	if err != nil {
		cleanup()
		return nil, err
	}
	fail := func(err error) (*ProcessGroup, error) { guard.release(); cleanup(); return nil, err }
	guard.configure(cmd)
	if cmd.WaitDelay == 0 {
		cmd.WaitDelay = PipelineWaitDelay
	}
	if err := confine.ValidateProcessInputs(cmd); err != nil {
		return fail(err)
	}
	if err := cmd.Start(); err != nil {
		return fail(err)
	}
	if err := guard.onStarted(cmd); err != nil {
		guard.kill(cmd)
		_ = cmd.Wait()
		return fail(fmt.Errorf("process group: %w", err))
	}
	return &ProcessGroup{command: cmd, guard: guard, cleanup: cleanup, done: make(chan struct{})}, nil
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
