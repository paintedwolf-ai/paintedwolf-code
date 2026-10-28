package command

import (
	"context"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools/native/toolkit"
)

// DefaultParkThreshold is how long after it starts a command call keeps its
// turn while its program waits. A call whose next wake falls later parks.
const DefaultParkThreshold = 10 * time.Second

// programBound is a call's outer deadline: its timeout_ms, or the catalog's
// program lifetime when the call names none.
type programBound struct {
	limitID string
	maximum time.Duration
}

func callBound(args map[string]any, catalog toolcontract.LanguageCatalog) programBound {
	if timeout := toolkit.ClampIntArg(args, "timeout_ms", 0, 0, 7200000); timeout > 0 {
		return programBound{limitID: "timeout_ms", maximum: time.Duration(timeout) * time.Millisecond}
	}
	for _, limit := range catalog.Language.Limits {
		if limit.ID == "program_lifetime_ms" {
			return programBound{limitID: limit.ID, maximum: time.Duration(limit.Maximum) * time.Millisecond}
		}
	}
	return programBound{}
}

// refusal is the limit a single wait reaches when it cannot end within the
// bound; observed is how long after the program's start the wait would end.
func (b programBound) refusal(observed time.Duration) error {
	return &toolrejection.ToolReject{Code: "COMMAND_LIMIT_EXCEEDED", Data: map[string]any{
		"limit_id": b.limitID, "resource_unit": "milliseconds",
		"maximum": b.maximum.Milliseconds(), "observed": observed.Milliseconds(),
	}}
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
	end      func()
	detach   func() bool
}

func (r *ProgramRuntime) newProgramCall(ctx context.Context, args map[string]any, catalog toolcontract.LanguageCatalog) *programCall {
	call := &programCall{started: r.clock().Now(), bound: callBound(args, catalog)}
	programCtx, cancel := context.WithCancelCause(context.WithoutCancel(ctx))
	call.detach = context.AfterFunc(ctx, func() { cancel(context.Cause(ctx)) })
	call.end = func() { cancel(nil) }
	if call.bound.maximum > 0 {
		var cancelDeadline context.CancelFunc
		call.deadline = time.Now().Add(call.bound.maximum)
		programCtx, cancelDeadline = context.WithDeadline(programCtx, call.deadline)
		call.end = func() { cancelDeadline(); cancel(nil) }
	}
	call.ctx = programCtx
	return call
}

// programPark is one call's watch over its program's waits. It refuses a wait
// the bound cannot hold and signals, once, a wait that should park the call.
type programPark struct {
	clock     commandprogram.Clock
	started   time.Time
	threshold time.Duration
	bound     programBound
	// parks carries the wait state that parks the call.
	parks chan commandprogram.WaitState

	mu        sync.Mutex
	blocked   *blockedWait
	decided   bool
	stopTimer func() bool
}

type blockedWait struct{ state commandprogram.WaitState }

var _ commandprogram.ProgramWaits = (*programPark)(nil)

func newProgramPark(clock commandprogram.Clock, call *programCall, threshold time.Duration) *programPark {
	return &programPark{clock: clock, started: call.started, threshold: threshold, bound: call.bound, parks: make(chan commandprogram.WaitState, 1)}
}

func (p *programPark) Block(_ context.Context, state commandprogram.WaitState) (func(), error) {
	if p.bound.maximum > 0 && !state.Until.IsZero() && state.Until.After(p.started.Add(p.bound.maximum)) {
		return nil, p.bound.refusal(state.Until.Sub(p.started))
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

// considerLocked parks a wait that ends after the inline budget. A wait only
// jobs can end has no known wake, so it parks if it still holds the program
// when the budget runs out.
func (p *programPark) considerLocked(blocked *blockedWait) {
	if p.decided {
		return
	}
	p.stopTimerLocked()
	inline := p.started.Add(p.threshold)
	until := blocked.state.Until
	switch now := p.clock.Now(); {
	case !until.IsZero() && !until.After(inline):
	case until.IsZero() && now.Before(inline):
		p.stopTimer = p.clock.AfterFunc(inline.Sub(now), func() {
			p.mu.Lock()
			defer p.mu.Unlock()
			if p.blocked == blocked {
				p.parkLocked(blocked.state)
			}
		})
	default:
		p.parkLocked(blocked.state)
	}
}

func (p *programPark) parkLocked(state commandprogram.WaitState) {
	if p.decided {
		return
	}
	p.decided = true
	p.parks <- state
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
	p.decided = true
	p.stopTimerLocked()
}
