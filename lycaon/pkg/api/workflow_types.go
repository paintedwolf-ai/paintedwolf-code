package api

type WorkflowRunStatus string

const (
	WorkflowRunStatusRunning       WorkflowRunStatus = "running"
	WorkflowRunStatusPaused        WorkflowRunStatus = "paused"
	WorkflowRunStatusPausedOnChild WorkflowRunStatus = "paused_on_child"
	WorkflowRunStatusComplete      WorkflowRunStatus = "complete"
	WorkflowRunStatusFailed        WorkflowRunStatus = "failed"
	WorkflowRunStatusCanceled      WorkflowRunStatus = "canceled"
	// WorkflowRunStatusInterrupted closes orphaned catalog or child runs.
	// Session-ambient roots persist across idle gaps.
	WorkflowRunStatusInterrupted WorkflowRunStatus = "interrupted"
)

// WorkflowRunObligation status vocabulary.
const (
	ObligationStatusOff      = "off"
	ObligationStatusEmpty    = "empty"
	ObligationStatusPending  = "pending"
	ObligationStatusComplete = "complete"
	ObligationStatusFailed   = "failed"
)

// WorkflowScope identifies which tier supplied a catalog manifest.
type WorkflowScope string

const (
	WorkflowScopeBundled WorkflowScope = "bundled"
	WorkflowScopeProject WorkflowScope = "project"
	WorkflowScopeSession WorkflowScope = "session"
)
