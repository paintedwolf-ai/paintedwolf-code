package exec

import (
	"context"
	"fmt"
	"io"
	"sync"
	"syscall"
)

// AsyncPipeline runs a command plan without blocking the caller.
type AsyncPipeline struct {
	run          *sequenceRun
	done         chan struct{}
	result       *PipelineResult
	err          error
	cancel       context.CancelFunc
	completionMu sync.Mutex
	completed    bool
	waiters      map[*pipelineWaiter]bool
}

// StartPipelineAsync launches stages and returns immediately. stdout/stderr receive
// incremental process output; Kill terminates the whole pipeline tree.
func StartPipelineAsync(
	parentCtx context.Context,
	stages []Stage,
	opts ExecOpts,
	stdout, stderr io.Writer,
) (*AsyncPipeline, error) {
	if err := opts.Launch.validate(); err != nil {
		return nil, err
	}
	if err := validatePipelineStages(stages); err != nil {
		return nil, err
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}

	runCtx, cancel := context.WithCancel(parentCtx)

	stdinReader, err := openStdin(opts.Stdin)
	if err != nil {
		cancel()
		return nil, err
	}

	redirect, err := openRedirectFiles(opts.Redirect)
	if err != nil {
		cancel()
		if stdinReader != nil {
			_ = stdinReader.Close()
		}
		return nil, err
	}

	// The first group starts before the handle is returned, so a launch failure
	// reaches the caller as an error instead of an immediate completion.
	run := newSequenceRun(stages, opts, newCallStreams(opts, stdout, stderr, redirect), stdinReader, PipelineWaitDelay)
	if err := run.start(runCtx); err != nil {
		redirect.close()
		if stdinReader != nil {
			_ = stdinReader.Close()
		}
		cancel()
		return nil, err
	}

	async := &AsyncPipeline{
		run:    run,
		done:   make(chan struct{}),
		cancel: cancel,
	}
	if opts.Started != nil {
		opts.Started(async.PID())
	}
	go async.wait(runCtx, opts, stdinReader, redirect)
	return async, nil
}

func (a *AsyncPipeline) wait(runCtx context.Context, opts ExecOpts, stdinReader io.ReadCloser, redirect redirectFiles) {
	defer a.complete()
	defer a.cancel()
	defer redirect.close()
	if stdinReader != nil {
		defer func() { _ = stdinReader.Close() }()
	}

	maxOut := opts.MaxOutputBytes
	if maxOut <= 0 {
		maxOut = DefaultMaxOutputBytes
	}
	a.run.drive(runCtx)
	a.result, a.err = a.run.finalize(nil, false, maxOut)
	// The commit outlives the run deadline so output written before it still lands.
	if err := redirect.commit(context.WithoutCancel(runCtx)); err != nil {
		a.err = fmt.Errorf("%w: %w", ErrRedirectCommit, err)
	}
}

// Kill terminates the running tree, SIGKILL after TerminateGrace, and stops
// the plan from starting another group.
func (a *AsyncPipeline) Kill() {
	if a == nil {
		return
	}
	a.run.terminate()
	if a.cancel != nil {
		a.cancel()
	}
}

// Signal delivers sig to the running tree without terminating it; SIGKILL
// ends it at once. It reports whether a running stage received it.
func (a *AsyncPipeline) Signal(sig syscall.Signal) bool {
	if a == nil {
		return false
	}
	return a.run.signal(sig)
}

// Wait blocks until the pipeline finishes and returns its result.
func (a *AsyncPipeline) Wait() (*PipelineResult, error) {
	if a == nil {
		return nil, nil
	}
	<-a.done
	return a.result, a.err
}

// Done exposes completion without waiting for the result payload.
func (a *AsyncPipeline) Done() <-chan struct{} {
	if a == nil {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	return a.done
}

// PID returns the OS process ID of the launched stage, or 0.
func (a *AsyncPipeline) PID() int {
	if a == nil || a.run == nil {
		return 0
	}
	return a.run.pid()
}

type pipelineWaiter struct{ unblock func() }

// ObserveWait publishes blocking only while this pipeline is incomplete.
// Completion wakes observers before making its terminal result available.
func (a *AsyncPipeline) ObserveWait(block func() (func(), error)) (func(), error) {
	a.completionMu.Lock()
	defer a.completionMu.Unlock()
	if a.completed {
		return func() {}, nil
	}
	unblock, err := block()
	if err != nil {
		return nil, err
	}
	waiter := &pipelineWaiter{unblock: unblock}
	if a.waiters == nil {
		a.waiters = map[*pipelineWaiter]bool{}
	}
	a.waiters[waiter] = true
	return func() {
		a.completionMu.Lock()
		defer a.completionMu.Unlock()
		if a.waiters[waiter] {
			unblock()
			delete(a.waiters, waiter)
		}
	}, nil
}
func (a *AsyncPipeline) complete() {
	a.completionMu.Lock()
	defer a.completionMu.Unlock()
	a.completed = true
	for waiter := range a.waiters {
		waiter.unblock()
		delete(a.waiters, waiter)
	}
	close(a.done)
}
