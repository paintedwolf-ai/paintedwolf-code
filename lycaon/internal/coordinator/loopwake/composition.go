package loopwake

type LoopEngine struct {
	Subscriptions *WaitSubscriptions
	Deliveries    *WaitDeliveries
	Policy        *HostWakePolicy
	Facts         *SessionFacts
	Observations  *PromptObservations
	Cycles        *WorkerCycles
	Turns         *HostTurns
	Waits         *Waits
	Nudges        *Nudges
	Admission     *Admission
}

func NewLoopEngine() *LoopEngine {
	l := &LoopEngine{Subscriptions: &WaitSubscriptions{}, Deliveries: &WaitDeliveries{}, Policy: &HostWakePolicy{}, Facts: &SessionFacts{}, Observations: &PromptObservations{}, Cycles: &WorkerCycles{}, Turns: &HostTurns{}, Waits: &Waits{}, Nudges: &Nudges{}, Admission: &Admission{}}
	l.Subscriptions.Nudges = l.Nudges
	l.Subscriptions.Facts = l.Facts
	l.Subscriptions.Deliveries = l.Deliveries
	l.Subscriptions.Waits = l.Waits
	l.Subscriptions.Cycles = l.Cycles
	l.Deliveries.Admission = l.Admission
	l.Deliveries.Turns = l.Turns
	l.Deliveries.Subscriptions = l.Subscriptions
	l.Policy.Facts = l.Facts
	l.Policy.Subscriptions = l.Subscriptions
	l.Policy.Waits = l.Waits
	l.Policy.Cycles = l.Cycles
	l.Observations.Nudges = l.Nudges
	l.Observations.Deliveries = l.Deliveries
	l.Observations.Subscriptions = l.Subscriptions
	l.Cycles.Nudges = l.Nudges
	l.Cycles.Subscriptions = l.Subscriptions
	l.Cycles.Waits = l.Waits
	l.Turns.Admission = l.Admission
	l.Turns.Policy = l.Policy
	l.Turns.Nudges = l.Nudges
	l.Turns.Observations = l.Observations
	l.Turns.Facts = l.Facts
	l.Turns.Subscriptions = l.Subscriptions
	l.Turns.Waits = l.Waits
	l.Turns.Cycles = l.Cycles
	l.Waits.Policy = l.Policy
	l.Waits.Nudges = l.Nudges
	l.Waits.Facts = l.Facts
	l.Waits.Deliveries = l.Deliveries
	l.Waits.Subscriptions = l.Subscriptions
	l.Waits.Cycles = l.Cycles
	l.Nudges.Admission = l.Admission
	l.Nudges.Turns = l.Turns
	l.Nudges.Policy = l.Policy
	l.Nudges.Observations = l.Observations
	l.Nudges.Facts = l.Facts
	l.Nudges.Deliveries = l.Deliveries
	l.Nudges.Subscriptions = l.Subscriptions
	l.Nudges.Waits = l.Waits
	l.Nudges.Cycles = l.Cycles
	l.Admission.Turns = l.Turns
	l.Admission.Nudges = l.Nudges
	l.Admission.Facts = l.Facts
	l.Subscriptions.Policy = l.Policy
	return l
}
func (l *LoopEngine) SetDeps(deps LoopDeps) {
	if l == nil {
		return
	}
	l.Subscriptions.setDeps(WaitSubscriptionsDeps{GetSession: deps.GetSession, HostWakeOverlayPromoteDue: deps.HostWakeOverlayPromoteDue, ProcessReport: deps.ProcessReport, ProcessRunning: deps.ProcessRunning, ProcessState: deps.ProcessState, ScanCycleOpen: deps.ScanCycleOpen, WorkerCycleIdle: deps.WorkerCycleIdle})
	l.Deliveries.setDeps(WaitDeliveriesDeps{RunWaitResume: deps.RunWaitResume})
	l.Policy.setDeps(HostWakePolicyDeps{DropPendingKicksBeforeBatchSeq: deps.DropPendingKicksBeforeBatchSeq, DropPendingKicksForBatchSeq: deps.DropPendingKicksForBatchSeq, HostWakeActionable: deps.HostWakeActionable, WorkflowSource: deps.WorkflowSource})
	l.Facts.setDeps(SessionFactsDeps{GetSession: deps.GetSession, Limits: deps.Limits, WorkflowObligationsOpen: deps.WorkflowObligationsOpen, WorkflowSource: deps.WorkflowSource})
	l.Cycles.setDeps(WorkerCyclesDeps{GetSession: deps.GetSession, WorkerCycleIdle: deps.WorkerCycleIdle})
	l.Turns.setDeps(HostTurnsDeps{HostTurnBlocked: deps.HostTurnBlocked, RunPrompt: deps.RunPrompt})
	l.Waits.setDeps(WaitsDeps{PublishWaitLease: deps.PublishWaitLease})
	l.Nudges.setDeps(NudgesDeps{BoardWillForceInject: deps.BoardWillForceInject, CoordinatorFrame: deps.CoordinatorFrame, GetSession: deps.GetSession, HasQueuedKick: deps.HasQueuedKick, OnLoopQuiescent: deps.OnLoopQuiescent, QueueInform: deps.QueueInform})
	l.Admission.setDeps(AdmissionDeps{GetSession: deps.GetSession, IsCoordinatorSession: deps.IsCoordinatorSession, IsEscalated: deps.IsEscalated, Limits: deps.Limits})
}
