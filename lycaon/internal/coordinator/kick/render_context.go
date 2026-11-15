package kick

import "github.com/lycaon/lycaon/internal/toolsurface"

// CoordinatorKickRenderContext carries prompt-time host state.
type CoordinatorKickRenderContext struct {
	// ToolPlan is what the model call carrying this kick can reach.
	ToolPlan     toolsurface.Plan
	WorkflowID   string
	CurrentPhase string
	BatchPhase   string
	BatchSeq     int
	// PendingOverlays counts results awaiting landing; the roster names them.
	PendingOverlays int
	// PartialWorkers counts workers whose latest report is partial.
	PartialWorkers     int
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
