package loopwake

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

const (
	parkedState      = "parked"
	waitResourceKind = "wait"
	// parkedProgramGrace keeps a parked program's lease armed past the
	// program's own bound, so the program's deadline result settles it.
	parkedProgramGrace = time.Minute
)

// parkRequest is one call parking its turn on the session's wait lease.
type parkRequest struct {
	operation      string
	reason         string
	until          time.Time
	untilComplete  bool
	conditions     []awaitstore.Condition
	triggers       []WaitTrigger
	processHandles []string
	workerHandles  []string
}

// park arms the session's single wait lease for the calling turn and ends the
// turn on it. Wait calls and parked programs share it: one lease, one sleep
// projection, one delivery path.
func (l *WaitSubscriptions) park(ctx context.Context, tctx tools.ToolContext, request parkRequest) (awaitstore.Lease, error) {
	sessionID := tctx.Identity.SessionID
	worker := strings.TrimSpace(tctx.Identity.WorkerJobID) != ""
	var lease awaitstore.Lease
	if store := l.durableWaitStore(); store != nil {
		var err error
		lease, err = store.Arm(ctx, awaitstore.Lease{
			SessionID: sessionID, RootSessionID: rootSessionID(tctx), ProjectID: tctx.Identity.ProjectID,
			ProjectDir: tctx.ActiveRootPath(), ToolCallID: tctx.Identity.ToolCallID, WorkerJobID: tctx.Identity.WorkerJobID,
			ProfileID: tctx.Identity.Agent, Deadline: request.until, UntilComplete: request.untilComplete, Conditions: request.conditions,
			LoopbackPorts: append([]uint16(nil), tctx.Local.LoopbackConnectPorts...), Reason: request.reason,
		})
		if err != nil {
			return awaitstore.Lease{}, err
		}
		if !worker {
			// Keep a result that settled this lease during registration.
			if winner, ok := l.Deliveries.waitWinner(sessionID); ok && winner.LeaseID != lease.ID {
				l.Deliveries.waitWinners.CompareAndDelete(strings.TrimSpace(sessionID), winner)
			}
		}
	}
	triggers := request.triggers
	if worker {
		// The durable worker queue schedules its own deadline.
		triggers = removeWaitTrigger(triggers, WaitTriggerTimer)
	}
	l.Waits.enterSleep(ctx, sessionID, sleepArm{
		until: request.until, untilComplete: request.untilComplete, reason: request.reason, triggers: triggers,
		processHandles: request.processHandles, workerHandles: request.workerHandles, mover: SleepMoverHost,
		caller: waitCaller{tool: request.operation, toolCallID: tctx.Identity.ToolCallID},
	})
	l.Waits.MarkWaitCalled(sessionID)
	if tctx.Effects.Out != nil {
		tctx.Effects.Out.OwnerRef = lease.ID
		tctx.Effects.Out.Completion = &api.ToolCompletion{
			Operation: request.operation, State: parkedState, ResourceKind: waitResourceKind,
			ResourceID: lease.ID, Reason: request.reason,
		}
	}
	return lease, nil
}

// ProgramParking parks command calls whose program waits. The program's own
// end resolves the lease and resumes the turn with its complete result; any
// other end of the wait interrupts the program first.
type ProgramParking struct{ Subscriptions *WaitSubscriptions }

var _ tools.ProgramParking = ProgramParking{}

// parkedProgram is one session's parked command call. Whoever removes it from
// the session's slot settles its lease: the program's end or an interruption.
type parkedProgram struct {
	loop        *WaitSubscriptions
	sessionID   string
	workerJobID string
	programID   string
	reason      string
	interrupt   func(context.Context) string
	// armed holds until the lease exists, so an early interruption settles it.
	armed   sync.Mutex
	leaseID string
}

// ParkProgram arms the session's lease on the program's completion once the
// program's wait is claimed. A wait that already ended, a session already
// holding a parked call, or a host without a durable wait store keeps the
// program in the foreground.
func (p ProgramParking) ParkProgram(ctx context.Context, tctx tools.ToolContext, park tools.ProgramPark) (tools.ParkedProgram, error) {
	l := p.Subscriptions
	sessionID := strings.TrimSpace(tctx.Identity.SessionID)
	if l == nil || l.durableWaitStore() == nil || sessionID == "" || park.ProgramID == "" || park.Claim == nil || park.Interrupt == nil {
		return nil, tools.ErrParkUnavailable
	}
	parked := &parkedProgram{
		loop: l, sessionID: sessionID, workerJobID: strings.TrimSpace(tctx.Identity.WorkerJobID),
		programID: park.ProgramID, reason: park.Reason, interrupt: park.Interrupt,
	}
	parked.armed.Lock()
	defer parked.armed.Unlock()
	if _, held := l.parkedPrograms.LoadOrStore(sessionID, parked); held {
		return nil, tools.ErrParkUnavailable
	}
	if !park.Claim() {
		l.parkedPrograms.CompareAndDelete(sessionID, parked)
		return nil, tools.ErrWaitEnded
	}
	lease, err := l.park(ctx, tctx, parkRequest{
		operation: "command", reason: park.Reason, until: park.Deadline.Add(parkedProgramGrace), untilComplete: true,
		conditions: []awaitstore.Condition{{Kind: string(WaitTriggerProgramDone), ProgramID: park.ProgramID}},
		triggers:   ensureTimerBackstop([]WaitTrigger{WaitTriggerProgramDone}),
	})
	if err != nil {
		l.parkedPrograms.CompareAndDelete(sessionID, parked)
		return nil, err
	}
	parked.leaseID = lease.ID
	return parked, nil
}

func (p *parkedProgram) LeaseID() string {
	p.armed.Lock()
	defer p.armed.Unlock()
	return p.leaseID
}

// Settle resolves the lease on the program's end and starts the resume.
func (p *parkedProgram) Settle(ctx context.Context, result string, err error) {
	if !p.loop.parkedPrograms.CompareAndDelete(p.sessionID, p) {
		return
	}
	winner := programWinner(p.programID, "completed", result)
	if err != nil {
		winner.Outcome, winner.Report = "failed", err.Error()
	}
	_, _ = p.loop.resolveLease(ctx, p.loop.durableWaitStore(), p.lease(), winner)
}

func (p *parkedProgram) lease() awaitstore.Lease {
	return awaitstore.Lease{ID: p.LeaseID(), SessionID: p.sessionID, WorkerJobID: p.workerJobID}
}

// programWinner is the condition a parked program's lease settles on.
func programWinner(programID, outcome, report string) awaitstore.Condition {
	condition := awaitstore.Condition{Kind: string(WaitTriggerProgramDone), ProgramID: programID, Outcome: outcome, Report: report}
	if outcome == "interrupted" {
		condition.Code = toolrejection.ToolOwnerInterruptedCode
	}
	return condition
}

// interruptParkedProgram ends the session's parked program at its waiting
// statement and settles its lease as interrupted with the program's result.
// With deliver set, the session's next turn opens with that result. It reports
// whether a parked program was live.
func (l *WaitSubscriptions) interruptParkedProgram(ctx context.Context, sessionID string, deliver bool) bool {
	if l == nil {
		return false
	}
	value, ok := l.parkedPrograms.LoadAndDelete(strings.TrimSpace(sessionID))
	if !ok {
		return false
	}
	parked := value.(*parkedProgram)
	leaseID := parked.LeaseID()
	winner := programWinner(parked.programID, "interrupted", parked.interrupt(ctx))
	if store := l.durableWaitStore(); store != nil && leaseID != "" {
		_, _ = store.SettleLease(context.WithoutCancel(ctx), leaseID, "interrupted", winner)
	}
	if deliver {
		l.Deliveries.rememberEndedWait(parked.sessionID, winner)
	}
	return true
}

// resolveLease settles a lease on the condition that ended it and starts its
// delivery: a coordinator resume turn, or the worker queue's own wake.
func (l *WaitSubscriptions) resolveLease(ctx context.Context, store *awaitstore.Store, lease awaitstore.Lease, winner awaitstore.Condition) (bool, error) {
	if store == nil || strings.TrimSpace(lease.ID) == "" {
		return false, nil
	}
	won, err := store.SettleLease(ctx, lease.ID, "resolved", winner)
	if err != nil || !won {
		return won, err
	}
	coordinator := strings.TrimSpace(lease.WorkerJobID) == ""
	if coordinator {
		l.Deliveries.rememberWaitWinner(lease.SessionID, lease.ID, winner)
	}
	l.Waits.breakSleep(ctx, lease.SessionID, winner.Kind, false)
	if coordinator {
		l.Nudges.Nudge(ctx, lease.SessionID, anchor.LoopWake, anchor.LoopWake, lease.ID, anchor.Envelope{})
	}
	return true, nil
}

// SetLostProgramResult installs the program owner's account of a program the
// host lost, composed from its durable receipts after a restart.
func (l *WaitSubscriptions) SetLostProgramResult(result func(ctx context.Context, sessionID, programID string) (string, error)) {
	if l == nil {
		return
	}
	l.depsMu.Lock()
	l.lostProgramResult = result
	l.depsMu.Unlock()
}

// recoverParkedProgram resolves the lease of a program the host lost: the
// program is never replayed, and the turn resumes with its partial result.
func (l *WaitSubscriptions) recoverParkedProgram(ctx context.Context, store *awaitstore.Store, lease awaitstore.Lease, programID string) error {
	l.depsMu.RLock()
	lost := l.lostProgramResult
	l.depsMu.RUnlock()
	report := ""
	if lost != nil {
		var err error
		if report, err = lost(ctx, lease.SessionID, programID); err != nil {
			// The lease still resolves, so the turn resumes without its receipts.
			slog.WarnContext(ctx, "parked program receipts unavailable", "session_id", lease.SessionID, "program_id", programID, "error", err)
			report = ""
		}
	}
	_, err := l.resolveLease(ctx, store, lease, programWinner(programID, "interrupted", report))
	return err
}

// leaseProgram returns the program a lease parks, if it parks one.
func leaseProgram(lease awaitstore.Lease) (string, bool) {
	for _, condition := range lease.Conditions {
		if condition.Kind == string(WaitTriggerProgramDone) && condition.ProgramID != "" {
			return condition.ProgramID, true
		}
	}
	return "", false
}
