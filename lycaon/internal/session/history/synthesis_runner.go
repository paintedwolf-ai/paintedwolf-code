package history

import (
	"context"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/workscope"
)

const synthesisConcurrency = 4
const synthesisLifetime = 15 * time.Minute

// SynthesisRunner bounds optional enrichment independently of baseline compaction.
type SynthesisRunner struct {
	mu     sync.Mutex
	active map[string]*synthesisRun
	slots  chan struct{}
	work   workscope.Group
}

func NewSynthesisRunner() *SynthesisRunner {
	return &SynthesisRunner{active: make(map[string]*synthesisRun), slots: make(chan struct{}, synthesisConcurrency)}
}

type synthesisRun struct {
	cancel context.CancelFunc
	next   func()
}

// Start declines duplicate or excess work.
func (r *SynthesisRunner) Start(parent context.Context, id string, run func(context.Context)) bool {
	return r.start(parent, id, run, false)
}

// StartLatest keeps one successor for a session whose baseline has advanced.
func (r *SynthesisRunner) StartLatest(parent context.Context, id string, run func(context.Context)) {
	r.start(parent, id, run, true)
}

func (r *SynthesisRunner) start(parent context.Context, id string, run func(context.Context), keepLatest bool) bool {
	if r == nil || id == "" || run == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if active := r.active[id]; active != nil {
		if keepLatest {
			active.next = func() { r.startLocked(parent, id, run) }
		}
		return false
	}
	return r.startLocked(parent, id, run)
}

func (r *SynthesisRunner) startLocked(parent context.Context, id string, run func(context.Context)) bool {
	select {
	case r.slots <- struct{}{}:
	default:
		return false
	}
	ctx, finish, err := r.work.Begin(context.WithoutCancel(parent))
	if err != nil {
		<-r.slots
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, synthesisLifetime)
	active := &synthesisRun{cancel: cancel}
	r.active[id] = active
	go func() {
		defer finish()
		defer observability.GuardPanic("session.compaction_synthesis")
		defer r.release(id, active)
		defer cancel()
		run(ctx)
	}()
	return true
}

func (r *SynthesisRunner) release(id string, active *synthesisRun) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.active, id)
	<-r.slots
	if active.next != nil {
		active.next()
	}
}

// Cancel prevents publication without waiting for an unresponsive provider.
func (r *SynthesisRunner) Cancel(id string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if active := r.active[id]; active != nil {
		active.next = nil
		active.cancel()
	}
}

func (r *SynthesisRunner) Stop() {
	if r != nil {
		r.work.Stop()
	}
}
func (r *SynthesisRunner) WaitContext(ctx context.Context) error {
	if r == nil {
		return nil
	}
	return r.work.Wait(ctx)
}
