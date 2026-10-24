package toolcontract

// ProgramProgressIdentity attributes an envelope update without inventing a
// shell node or source span. RootSessionID is the current progress-owner scope;
// Scope remains the original addressed project/session scope.
type ProgramProgressIdentity struct {
	ProgramID     string
	ExecutionID   string
	ActorID       string
	SessionID     string
	RootSessionID string
	Scope         OperationScope
}
