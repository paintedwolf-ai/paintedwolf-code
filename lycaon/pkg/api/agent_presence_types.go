package api

// AgentActivityKind is what a running tool call does to its file target.
type AgentActivityKind string

const (
	AgentActivityKindReading AgentActivityKind = "reading"
	AgentActivityKindEditing AgentActivityKind = "editing"
)

// AgentPresenceExtent is how a presence item's ranges cover its file.
type AgentPresenceExtent string

const (
	// AgentPresenceExtentRange ranges are whole-line spans.
	AgentPresenceExtentRange AgentPresenceExtent = "range"
	// AgentPresenceExtentWholeFile carries no ranges; the item covers the file.
	AgentPresenceExtentWholeFile AgentPresenceExtent = "whole_file"
	// AgentPresenceExtentMatches ranges are single-line character spans a search returned.
	AgentPresenceExtentMatches AgentPresenceExtent = "matches"
	// AgentPresenceExtentInsertion has one empty range where new text goes in.
	AgentPresenceExtentInsertion AgentPresenceExtent = "insertion"
)

// AgentIntentOperation is the mutation a pending write-family call performs.
type AgentIntentOperation string

const (
	AgentIntentOperationEdit   AgentIntentOperation = "edit"
	AgentIntentOperationWrite  AgentIntentOperation = "write"
	AgentIntentOperationCreate AgentIntentOperation = "create"
	AgentIntentOperationDelete AgentIntentOperation = "delete"
	AgentIntentOperationMove   AgentIntentOperation = "move"
)

// AgentIntentState is where a resolved, unlanded mutation stands.
type AgentIntentState string

const (
	AgentIntentStatePending          AgentIntentState = "pending"
	AgentIntentStateAwaitingApproval AgentIntentState = "awaiting_approval"
)

// AgentWorkerDraftState is where a worker's change to a primary file stands.
type AgentWorkerDraftState string

const (
	// AgentWorkerDraftStateReserved is a path reservation with no draft yet.
	AgentWorkerDraftStateReserved AgentWorkerDraftState = "reserved"
	// AgentWorkerDraftStateDrafting means the worker wrote the file in its private branch.
	AgentWorkerDraftStateDrafting AgentWorkerDraftState = "drafting"
	// AgentWorkerDraftStateReady means the job completed and the draft waits to land.
	AgentWorkerDraftStateReady AgentWorkerDraftState = "ready"
	// AgentWorkerDraftStateLanding means the coordinator is promoting the draft.
	AgentWorkerDraftStateLanding AgentWorkerDraftState = "landing"
)
