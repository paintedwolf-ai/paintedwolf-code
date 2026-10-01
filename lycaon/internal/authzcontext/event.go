package authzcontext

import "time"

// Event is one append-only authz decision row in the per-session hash chain.
type Event struct {
	ID          string
	SessionID   string
	EventSeq    int
	PrevHash    string
	RowHash     string
	HashVersion int
	RecordedAt  time.Time
	ContextSeq  int
	Action      EventAction
	Outcome     EventOutcome
	ResolvedBy  ResolvedBy
	// ResolverPersonID is the person whose action settled the event now.
	ResolverPersonID string
	ToolName         string
	RejectCode       string
	DetailJSON       string
	ConfigHash       string
}

// RecordInput collects machine state for an authz_events append.
type RecordInput struct {
	SessionID  string
	Action     EventAction
	Outcome    EventOutcome
	ResolvedBy ResolvedBy
	// ResolverPersonID is the person whose action settled the event now.
	ResolverPersonID string
	ToolName         string
	RejectCode       string
	Detail           DetailInput
}
