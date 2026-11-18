package command

import (
	"context"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

// programBound is a program's outer deadline: the catalog's program lifetime.
type programBound struct {
	limitID string
	maximum time.Duration
}

func callBound(catalog toolcontract.LanguageCatalog) programBound {
	for _, limit := range catalog.Language.Limits {
		if limit.ID == "program_lifetime_ms" {
			return programBound{limitID: limit.ID, maximum: time.Duration(limit.Maximum) * time.Millisecond}
		}
	}
	return programBound{}
}

// refusal is the limit a single wait reaches when it cannot end within the
// bound; observed is how long after the program's start the wait would end.
func (b programBound) refusal(observed time.Duration) *toolrejection.ToolReject {
	data := map[string]any{
		"limit_id": b.limitID, "resource_unit": "milliseconds",
		"maximum": b.maximum.Milliseconds(), "observed": observed.Milliseconds(),
	}
	return &toolrejection.ToolReject{Code: "COMMAND_LIMIT_EXCEEDED", Data: data}
}

// callDeadline is the cause that ends a program whose bound expired; waiting
// is the statement the program was waiting in then.
type callDeadline struct{ waiting string }

func (*callDeadline) Error() string { return "command program lifetime expired" }

// deadlineStop is the refusal for a program its bound ended after elapsed.
func (b programBound) deadlineStop(expired *callDeadline, elapsed time.Duration) *toolrejection.ToolReject {
	reject := b.refusal(elapsed)
	reject.Data["deadline_expired"] = true
	if expired.waiting != "" {
		reject.Data["waiting_statement"] = expired.waiting
	}
	return reject
}

// programCall ties one command call to its program. The program runs under
// the call's bound and follows the call's cancellation until the call parks.
type programCall struct {
	ctx context.Context
	// started is the program's start on the runtime clock.
	started time.Time
	// deadline is the program's bound on the host clock, which leases use.
	deadline time.Time
	bound    programBound
	park     *programPark
	end      func()
	detach   func() bool
}

func (r *ProgramRuntime) newProgramCall(ctx context.Context, catalog toolcontract.LanguageCatalog) (context.Context, *programCall) {
	clock := r.clock()
	call := &programCall{started: clock.Now(), bound: callBound(catalog)}
	call.park = newProgramPark(clock, call, r.parkThreshold())
	programCtx, cancel := context.WithCancelCause(context.WithoutCancel(ctx))
	call.detach = context.AfterFunc(ctx, func() { cancel(context.Cause(ctx)) })
	call.end = func() { cancel(nil) }
	if call.bound.maximum > 0 {
		// The bound expires on the program clock, like an in-program timeout,
		// so its cause tells the result which deadline ended the program.
		call.deadline = time.Now().Add(call.bound.maximum)
		programCtx = commandprogram.WithBound(programCtx, clock, call.bound.maximum)
		stop := clock.AfterFunc(call.bound.maximum, func() { cancel(&callDeadline{waiting: call.park.waiting()}) })
		call.end = func() { stop(); cancel(nil) }
	}
	call.ctx = programCtx
	return programCtx, call
}

// programPark is one call's watch over its program's waits. It refuses a wait
// the bound cannot hold and signals each wait that should park the call. A
// signal parks only through claim, which holds while that wait is still the
// program's current one, so a wait that ended before its signal was taken
// never parks the call.
type programPark struct {
	clock     commandprogram.Clock
	started   time.Time
	threshold time.Duration
	bound     programBound
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
	return &programPark{clock: clock, started: call.started, threshold: threshold, bound: call.bound, parks: make(chan *blockedWait, 1)}
}

// waiting is the statement of the wait that holds the program, if any.
func (p *programPark) waiting() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.blocked == nil {
		return ""
	}
	return p.blocked.state.Statement
}

// Block refuses at once a wait that would end after the program's bound.
func (p *programPark) Block(_ context.Context, state commandprogram.WaitState) (func(), error) {
	if p.bound.maximum > 0 && !state.Until.IsZero() && state.Until.After(p.started.Add(p.bound.maximum)) {
		reject := p.bound.refusal(state.Until.Sub(p.started))
		reject.Data["wait_refused"] = true
		reject.Data["waiting_statement"] = state.Statement
		return nil, reject
	}
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
