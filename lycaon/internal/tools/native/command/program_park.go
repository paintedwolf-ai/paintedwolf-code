package command

import (
	"context"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/commandprogram"
)

// programCall ties one command call to its program. The program follows the
// call's cancellation until the call parks.
type programCall struct {
	ctx context.Context
	// started is the program's start on the runtime clock.
	started time.Time
	park    *programPark
	end     func()
	detach  func() bool
}

func (r *ProgramRuntime) newProgramCall(ctx context.Context) (context.Context, *programCall) {
	clock := r.clock()
	call := &programCall{started: clock.Now()}
	call.park = newProgramPark(clock, call, r.parkThreshold())
	programCtx, cancel := context.WithCancelCause(context.WithoutCancel(ctx))
	call.detach = context.AfterFunc(ctx, func() { cancel(context.Cause(ctx)) })
	call.end = func() { cancel(nil) }
	call.ctx = programCtx
	return programCtx, call
}

// programPark is one call's watch over its program's waits. It signals each
// wait that should park the call. A
// signal parks only through claim, which holds while that wait is still the
// program's current one, so a wait that ended before its signal was taken
// never parks the call.
type programPark struct {
	clock     commandprogram.Clock
	started   time.Time
	threshold time.Duration
	// parks carries the latest wait that should park the call.
	parks chan *blockedWait

	mu        sync.Mutex
	blocked   *blockedWait
	parked    bool
	settled   bool
	stopTimer func() bool
}

// blockedWait is one reported wait; its identity is the claim token.
type blockedWait struct{ state commandprogram.WaitState }

var _ commandprogram.ProgramWaits = (*programPark)(nil)

func newProgramPark(clock commandprogram.Clock, call *programCall, threshold time.Duration) *programPark {
	return &programPark{clock: clock, started: call.started, threshold: threshold, parks: make(chan *blockedWait, 1)}
}

func (p *programPark) Block(_ context.Context, state commandprogram.WaitState) (func(), error) {
	blocked := &blockedWait{state: state}
	p.mu.Lock()
	p.blocked = blocked
	p.considerLocked(blocked)
	p.mu.Unlock()
	return func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.blocked == blocked {
			p.blocked = nil
			p.stopTimerLocked()
		}
	}, nil
}

// considerLocked signals a wait that ends after the inline budget. A wait only
// jobs can end has no known wake, so it signals if it still holds the program
// when the budget runs out.
func (p *programPark) considerLocked(blocked *blockedWait) {
	if p.parked || p.settled {
		return
	}
	p.stopTimerLocked()
	inline := p.started.Add(p.threshold)
	now := p.clock.Now()
	waitInline := inline
	if later := now.Add(p.threshold); later.After(waitInline) {
		waitInline = later
	}
	until := blocked.state.Until
	switch {
	case !until.IsZero() && !until.After(waitInline):
	case until.IsZero() && now.Before(inline):
		p.stopTimer = p.clock.AfterFunc(inline.Sub(now), func() {
			p.mu.Lock()
			defer p.mu.Unlock()
			if p.blocked == blocked {
				p.signalLocked(blocked)
			}
		})
	default:
		p.signalLocked(blocked)
	}
}

// signalLocked replaces any untaken signal with this wait's.
func (p *programPark) signalLocked(blocked *blockedWait) {
	if p.parked || p.settled {
		return
	}
	select {
	case <-p.parks:
	default:
	}
	p.parks <- blocked
}

// claim takes the park for a signaled wait that still holds the program.
func (p *programPark) claim(blocked *blockedWait) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.parked || p.settled || p.blocked != blocked {
		return false
	}
	p.parked = true
	p.stopTimerLocked()
	return true
}

// release returns a claim whose park did not happen; later waits signal again.
func (p *programPark) release() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.parked = false
}

func (p *programPark) stopTimerLocked() {
	if p.stopTimer != nil {
		p.stopTimer()
		p.stopTimer = nil
	}
}

// settle ends the watch once the program ended in the foreground.
func (p *programPark) settle() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.settled = true
	p.stopTimerLocked()
}
