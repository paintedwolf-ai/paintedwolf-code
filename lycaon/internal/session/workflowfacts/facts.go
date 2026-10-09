package workflowfacts

type ActiveWorkflowManifest struct {
	CoordinatorProfile string
	Rules              []string
	HostPhaseAdvance   bool
	// Archive names the sealed version a retired run reads its guidance from.
	Archive string
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
type WorkflowCloseoutGateState struct {
	Gated      bool
	Phase      string
	OpenLeaves []string
}
type ResolvedWorkflowRequest struct {
	RunID            string
	OpeningMessageID string
	Text             string
}
