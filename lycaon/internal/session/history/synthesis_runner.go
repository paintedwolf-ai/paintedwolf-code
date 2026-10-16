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
	active map[string]context.CancelFunc
	slots  chan struct{}
	work   workscope.Group
}

func NewSynthesisRunner() *SynthesisRunner {
	return &SynthesisRunner{active: make(map[string]context.CancelFunc), slots: make(chan struct{}, synthesisConcurrency)}
}

// Start declines duplicate or excess work without queuing it.
func (r *SynthesisRunner) Start(parent context.Context, id string, run func(context.Context)) bool {
	if r == nil || id == "" || run == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active[id] != nil {
		return false
	}
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
	r.active[id] = cancel
	go func() {
		defer observability.GuardPanic("session.compaction_synthesis")
		defer finish()
		defer cancel()
		defer func() { r.mu.Lock(); delete(r.active, id); r.mu.Unlock(); <-r.slots }()
		run(ctx)
	}()
	return true
}

// Cancel prevents publication without waiting for an unresponsive provider.
func (r *SynthesisRunner) Cancel(id string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if cancel := r.active[id]; cancel != nil {
		cancel()
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
