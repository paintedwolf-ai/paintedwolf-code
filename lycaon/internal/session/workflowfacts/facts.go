package workflowfacts

type ActiveWorkflowManifest struct {
	CoordinatorProfile string
	Rules              []string
	HostPhaseAdvance   bool
}

type WorkflowCloseoutGateState struct {
	Gated      bool
	Phase      string
	OpenLeaves []string
}

type WorkflowPhaseGuardState struct {
	Phase string
	// ReportPhaseDeclared marks the phase that produces the run report.
	ReportPhaseDeclared   bool
	ReportCloseoutPending bool
	// PendingObligationKinds identifies unsettled phase work.
	PhaseObligationPending bool
	PendingObligationKinds []string
}

type ResolvedWorkflowRequest struct {
	RunID            string
	OpeningMessageID string
	Text             string
}
