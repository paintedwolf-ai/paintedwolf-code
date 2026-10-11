package loopwake

import (
	"context"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/scaffoldvars"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/pkg/api"
)

// loopDepsForTest returns idle coordinator defaults.
func loopDepsForTest() LoopDeps {
	return LoopDeps{
		IsCoordinatorSession: func(context.Context, *api.Session) bool { return true },
		Limits:               func(context.Context, *api.Session) settings.SessionLimits { return settings.DefaultSessionLimits() },
		WorkerCycleIdle:      func(context.Context, *api.Session, string) (bool, error) { return true, nil },
		QueueInform:          func(context.Context, string, anchor.ID, anchor.Envelope) {},
	}
}

type StubLoopWF struct {
	run                *api.WorkflowRun
	vars               map[string]any
	hostObligationHeld bool
	hostObligationKind string
}

func (s StubLoopWF) ActiveBySession(context.Context, string) (*api.WorkflowRun, error) {
	return s.run, nil
}

func (s StubLoopWF) GetScaffoldVars(context.Context, string) (map[string]any, error) {
	if s.vars != nil {
		return s.vars, nil
	}
	return map[string]any{}, nil
}

func (s StubLoopWF) HumanApprovalAwaiting(context.Context, string) (bool, error) {
	return scaffoldvars.HumanApprovalAwaiting(s.vars), nil
}

func (s StubLoopWF) HostObligationHeld(context.Context, string) (bool, error) {
	return s.hostObligationHeld, nil
}

func (s StubLoopWF) HostObligationHoldKinds(context.Context, string) []string {
	if !s.hostObligationHeld || s.hostObligationKind == "" {
		return nil
	}
	return []string{s.hostObligationKind}
}

func loopDepsSnapshotForTest(l *LoopEngine) LoopDeps {
	var deps LoopDeps
	deps.GetSession = l.Subscriptions.loopDeps().GetSession
	deps.HostWakeOverlayPromoteDue = l.Subscriptions.loopDeps().HostWakeOverlayPromoteDue
	deps.ProcessReport = l.Subscriptions.loopDeps().ProcessReport
	deps.ProcessRunning = l.Subscriptions.loopDeps().ProcessRunning
	deps.ProcessState = l.Subscriptions.loopDeps().ProcessState
	deps.ScanCycleOpen = l.Subscriptions.loopDeps().ScanCycleOpen
	deps.WorkerCycleIdle = l.Subscriptions.loopDeps().WorkerCycleIdle
	deps.RunWaitResume = l.Deliveries.loopDeps().RunWaitResume
	deps.DropPendingKicksBeforeBatchSeq = l.Policy.loopDeps().DropPendingKicksBeforeBatchSeq
	deps.DropPendingKicksForBatchSeq = l.Policy.loopDeps().DropPendingKicksForBatchSeq
	deps.HostWakeActionable = l.Policy.loopDeps().HostWakeActionable
	deps.WorkflowSource = l.Policy.loopDeps().WorkflowSource
	deps.Limits = l.Facts.loopDeps().Limits
	deps.WorkflowObligationsOpen = l.Facts.loopDeps().WorkflowObligationsOpen
	deps.HostTurnBlocked = l.Turns.loopDeps().HostTurnBlocked
	deps.RunPrompt = l.Turns.loopDeps().RunPrompt
	deps.PublishWaitLease = l.Waits.loopDeps().PublishWaitLease
	deps.BoardWillForceInject = l.Nudges.loopDeps().BoardWillForceInject
	deps.CoordinatorFrame = l.Nudges.loopDeps().CoordinatorFrame
	deps.HasQueuedKick = l.Nudges.loopDeps().HasQueuedKick
	deps.OnLoopQuiescent = l.Nudges.loopDeps().OnLoopQuiescent
	deps.QueueInform = l.Nudges.loopDeps().QueueInform
	deps.IsCoordinatorSession = l.Admission.loopDeps().IsCoordinatorSession
	deps.IsEscalated = l.Admission.loopDeps().IsEscalated
	return deps
}
