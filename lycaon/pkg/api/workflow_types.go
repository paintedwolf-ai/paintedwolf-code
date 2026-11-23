package api

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
