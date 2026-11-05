package kick

import "github.com/lycaon/lycaon/internal/toolsurface"

// CoordinatorKickRenderContext carries prompt-time host state.
type CoordinatorKickRenderContext struct {
	// ToolPlan is what the model call carrying this kick can reach.
	ToolPlan toolsurface.Plan
	WorkflowID         string
	CurrentPhase       string
	BatchPhase         string
	BatchSeq           int
	PendingOverlayJobs []string
	PartialWorkerJobs  []string
	PromotedPaths      []string
	AdvanceWhenGateMet string
	FailedLeaves       []string
	GateObligations    []GateObligation
	// Progress fields keep guidance behind the checklist latch.
	ProgressOpenItems    int
	ProgressClosureArmed bool
}

// GateObligation is a compact gate-feedback projection for kick templates.
type GateObligation struct {
	ID       string
	Purpose  string
	Satisfy  []string
	Missing  []string
	Required []string
}
