package session

import (
	"context"
	"sync"

	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/workscope"
)

// CompactionRunner coalesces background compaction by session.
type CompactionRunner struct {
	mu      sync.Mutex
	running map[string]*compactionRun
	pending map[string]bool
	active  map[string]*compactionRun
	work    workscope.Group
}

type compactionRun struct {
	cancel context.CancelFunc
	done   chan struct{}
	finish func()
}

// NewCompactionRunner constructs a background compaction scheduler.
func NewCompactionRunner() *CompactionRunner {
	return &CompactionRunner{
		running: make(map[string]*compactionRun),
		pending: make(map[string]bool),
		active:  make(map[string]*compactionRun),
	}
}

// Trigger schedules one pass or one pending rerun.
func (r *CompactionRunner) Trigger(parent context.Context, sessionID string, run func(ctx context.Context)) {
	if r == nil || sessionID == "" || run == nil {
		return
	}
	r.mu.Lock()
	if r.running[sessionID] != nil {
		r.pending[sessionID] = true
		r.mu.Unlock()
		return
	}
	bg, finish, err := r.work.Begin(context.WithoutCancel(parent))
	if err != nil {
		r.mu.Unlock()
		return
	}
	bg, cancel := context.WithCancel(bg)
	work := &compactionRun{cancel: cancel, done: make(chan struct{})}
	r.running[sessionID] = work
	r.mu.Unlock()
	go func() {
		defer finish()
		defer observability.GuardPanic("session.compaction_runner")
		defer cancel()
		defer close(work.done)
		defer func() {
			r.mu.Lock()
			if r.running[sessionID] == work {
				delete(r.running, sessionID)
				delete(r.pending, sessionID)
			}
			r.mu.Unlock()
		}()
		for {
			run(bg)
			r.mu.Lock()
			if bg.Err() != nil || !r.pending[sessionID] {
				delete(r.running, sessionID)
				delete(r.pending, sessionID)
				r.mu.Unlock()
				return
			}
			delete(r.pending, sessionID)
			r.mu.Unlock()
		}
	}()
}

// CancelSession cancels and joins a session's compaction pass.
func (r *CompactionRunner) CancelSession(sessionID string) {
	if r == nil || sessionID == "" {
		return
	}
	r.mu.Lock()
	work := r.running[sessionID]
	active := r.active[sessionID]
	if active != nil {
		active.cancel()
	}
	delete(r.pending, sessionID)
	if work != nil {
		work.cancel()
	}
	r.mu.Unlock()
	if work != nil {
		<-work.done
	}
	if active != nil {
		<-active.done
	}
}

// Wait blocks until all in-flight compaction passes finish.
func (r *CompactionRunner) Wait() {
	if r == nil {
		return
	}
	_ = r.work.Wait(context.Background())
}

// Execute serializes manual and background passes before either reads history.
// Cancellation and forgetting a session also join a manually requested pass.
func (r *CompactionRunner) Execute(ctx context.Context, sessionID string, run func(context.Context) error) error {
	work, workCtx, err := r.admit(ctx, sessionID)
	if err != nil {
		return err
	}
	defer func() {
		work.cancel()
		r.mu.Lock()
		delete(r.active, sessionID)
		close(work.done)
		r.mu.Unlock()
		work.finish()
	}()
	return run(workCtx)
}

// admit waits out any pass already running for the session, then registers this one.
func (r *CompactionRunner) admit(ctx context.Context, sessionID string) (*compactionRun, context.Context, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		r.mu.Lock()
		if active := r.active[sessionID]; active != nil {
			r.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			case <-active.done:
				continue
			}
		}
		activeCtx, finish, err := r.work.Begin(ctx)
		if err != nil {
			r.mu.Unlock()
			return nil, nil, err
		}
		activeCtx, cancel := context.WithCancel(activeCtx)
		work := &compactionRun{cancel: cancel, done: make(chan struct{}), finish: finish}
		r.active[sessionID] = work
		r.mu.Unlock()
		return work, activeCtx, nil
	}
}

// Stop seals admission and cancels all scheduled and manual passes.
func (r *CompactionRunner) Stop() {
	if r != nil {
		r.work.Stop()
	}
}

// WaitContext drains compaction before its stores are released.
func (r *CompactionRunner) WaitContext(ctx context.Context) error {
	if r == nil {
		return nil
	}
	return r.work.Wait(ctx)
}
