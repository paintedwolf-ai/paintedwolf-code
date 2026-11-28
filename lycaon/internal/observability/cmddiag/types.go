// Package cmddiag implements opt-in, bounded development runtime diagnostics
// for Portable Command execution. It defaults off, observes immutable snapshots
// asynchronously on a bounded queue, never blocks or alters product execution,
// and retains only redacted metadata and owning identifiers.
package cmddiag

import (
	"context"
	"time"
)

// SchemaVersion is the wire schema version for emitted diagnostic records.
const SchemaVersion = 1

// Disposition classifies each emitted diagnostic finding.
type Disposition string

const (
	DispositionContradiction Disposition = "contradiction"
	DispositionIncomplete    Disposition = "incomplete"
	DispositionUnsupported   Disposition = "unsupported"
	DispositionOverflow      Disposition = "overflow"
)

// RuleID identifies one bounded diagnostic check.
type RuleID string

const (
	RuleDupOrConflictingTerminal     RuleID = "dup_or_conflicting_terminal"
	RuleMissingChildTerminal         RuleID = "missing_child_terminal"
	RuleStaleTerminalPresentation    RuleID = "stale_terminal_presentation"
	RuleOperationSourceContradiction RuleID = "operation_source_contradiction"
	RuleStopAdmissionAfterAck        RuleID = "stop_admission_after_ack"
	RuleCaptureOverflow              RuleID = "capture_overflow"
	RuleObservationLoss              RuleID = "observation_loss"
)

// Default bounds for async capture and emitted metadata.
const (
	DefaultQueueCapacity         = 64
	DefaultMaxExecutionsPerRead  = 256
	DefaultMaxIDsPerFinding      = 16
	DefaultMaxFieldBytes         = 256
	DefaultTaskTimeout           = 5 * time.Second
	DefaultCloseGrace            = 250 * time.Millisecond
	DefaultMaxDedupEntries       = 4096
	DefaultMaxSnapshotActivities = 16
	DefaultMaxSnapshotExecIDs    = 4
)

// Task is a lightweight bounded work item enqueued onto the async worker
// before any store or receipt reads occur. When the queue is saturated,
// Fetch is never invoked.
type Task struct {
	SessionID  string
	ProgramID  string
	ToolCallID string
	Fetch      func(ctx context.Context) (Observation, error)
}

// Observation is an immutable, metadata-only snapshot of a command program's
// owner facts. It deliberately excludes raw command text, arguments,
// environment variables, stdout/stderr, workspace paths, and file contents.
type Observation struct {
	SessionID               string                `json:"session_id,omitempty"`
	ProgramID               string                `json:"program_id,omitempty"`
	ParentReceiptID         string                `json:"parent_receipt_id,omitempty"`
	ToolCallID              string                `json:"tool_call_id,omitempty"`
	ProgramState            string                `json:"program_state,omitempty"`
	ProgramStatusCode       *int                  `json:"program_status_code,omitempty"`
	ProgramStopCode         string                `json:"program_stop_code,omitempty"`
	ProgramSettled          bool                  `json:"program_settled"`
	Truncated               bool                  `json:"truncated,omitempty"`
	ExecutionsOmitted       int                   `json:"executions_omitted,omitempty"`
	PageInconsistent        bool                  `json:"page_inconsistent,omitempty"`
	LookupFailedExecutions  []string              `json:"lookup_failed_executions,omitempty"`
	ActivityRevision        int64                 `json:"activity_revision,omitempty"`
	ConsumerCaughtUp        bool                  `json:"consumer_caught_up"`
	PresentationUnsupported bool                  `json:"presentation_unsupported,omitempty"`
	Presentation            *PresentationSnapshot `json:"presentation,omitempty"`
	Executions              []ExecutionFact       `json:"executions,omitempty"`
	TerminalRecords         []TerminalFact        `json:"terminal_records,omitempty"`
	LinkedOperations        []OperationSourceLink `json:"linked_operations,omitempty"`
	StopAck                 *StopAckWitness       `json:"stop_ack,omitempty"`
	WEA                     *WEAObservation       `json:"wea,omitempty"`
}

// ExecutionFact retains bounded metadata for one child execution receipt.
type ExecutionFact struct {
	ExecutionID       string `json:"execution_id"`
	ParentExecutionID string `json:"parent_execution_id,omitempty"`
	OperationID       string `json:"operation_id,omitempty"`
	Owner             string `json:"owner,omitempty"`
	State             string `json:"state"`
	Admitted          bool   `json:"admitted"`
	OwnerStarted      bool   `json:"owner_started,omitempty"`
	OwnerInvoked      bool   `json:"owner_invoked,omitempty"`
	StatusCode        *int   `json:"status_code,omitempty"`
	StopCode          string `json:"stop_code,omitempty"`
	HostStopCode      string `json:"host_stop_code,omitempty"`
	EffectCertainty   string `json:"effect_certainty,omitempty"`
	SourceRef         string `json:"source_ref,omitempty"`
	AcceptedRevision  string `json:"accepted_revision,omitempty"`
	PublishedRevision string `json:"published_revision,omitempty"`
	AdmissionOrdinal  int64  `json:"admission_ordinal,omitempty"`
}

// TerminalFact records one observed terminal fact for an execution identity.
type TerminalFact struct {
	ExecutionID     string `json:"execution_id"`
	State           string `json:"state"`
	StatusCode      *int   `json:"status_code,omitempty"`
	StopCode        string `json:"stop_code,omitempty"`
	HostStopCode    string `json:"host_stop_code,omitempty"`
	EffectCertainty string `json:"effect_certainty,omitempty"`
	Source          string `json:"source,omitempty"`
}

// PresentationSnapshot captures bounded terminal presentation metadata,
// either from an explicit consumer catch-up witness or a host-boundary
// publication snapshot (HostBoundaryOnly=true).
type PresentationSnapshot struct {
	ProgramState      string         `json:"program_state"`
	ProgramStatusCode *int           `json:"program_status_code,omitempty"`
	ProgramStopCode   string         `json:"program_stop_code,omitempty"`
	ActivityRevision  int64          `json:"activity_revision,omitempty"`
	ActivityCount     int            `json:"activity_count,omitempty"`
	HostBoundaryOnly  bool           `json:"host_boundary_only,omitempty"`
	Truncated         bool           `json:"truncated,omitempty"`
	Activities        []ActivityFact `json:"activities,omitempty"`
}

// ActivityFact retains bounded state for one presented program activity.
type ActivityFact struct {
	ActivityID   string   `json:"activity_id"`
	State        string   `json:"state"`
	StatusCode   *int     `json:"status_code,omitempty"`
	ExecutionIDs []string `json:"execution_ids,omitempty"`
}

// OperationSourceLink binds a completed operation receipt to an independently
// recorded source and revision identity for cross-fact verification.
type OperationSourceLink struct {
	ExecutionID         string `json:"execution_id"`
	OperationID         string `json:"operation_id"`
	SourceID            string `json:"source_id,omitempty"`
	ExpectedOperationID string `json:"expected_operation_id,omitempty"`
	ExpectedSourceID    string `json:"expected_source_id,omitempty"`
	ExpectedRevision    string `json:"expected_revision,omitempty"`
	ObservedRevision    string `json:"observed_revision,omitempty"`
	Completed           bool   `json:"completed"`
	CompleteEvidence    bool   `json:"complete_evidence"`
	UnsupportedReason   string `json:"unsupported_reason,omitempty"`
}

// StopAckWitness represents an explicit Stop acknowledgment and whether child
// admission ordinals are comparable to it (gated on ST01-ST03).
type StopAckWitness struct {
	Qualified                  bool   `json:"qualified"`
	AckOrdinal                 int64  `json:"ack_ordinal"`
	AdmissionWitnessComparable bool   `json:"admission_witness_comparable"`
	StopRef                    string `json:"stop_ref,omitempty"`
}

// Finding is one structured, self-identifying diagnostic emitted per rule and
// program (or for capture overflow).
type Finding struct {
	SchemaVersion int            `json:"schema_version"`
	Timestamp     time.Time      `json:"ts"`
	RuleID        RuleID         `json:"rule_id"`
	Disposition   Disposition    `json:"disposition"`
	SessionID     string         `json:"session_id,omitempty"`
	ProgramID     string         `json:"program_id,omitempty"`
	ParentReceipt string         `json:"parent_receipt_id,omitempty"`
	ToolCallID    string         `json:"tool_call_id,omitempty"`
	ExecutionIDs  []string       `json:"execution_ids,omitempty"`
	OperationIDs  []string       `json:"operation_ids,omitempty"`
	SourceIDs     []string       `json:"source_ids,omitempty"`
	Expected      map[string]any `json:"expected,omitempty"`
	Observed      map[string]any `json:"observed,omitempty"`
	Reason        string         `json:"reason,omitempty"`
	DroppedCount  int64          `json:"dropped_count,omitempty"`
}

// IsTerminalState reports whether state is a terminal program or execution state.
func IsTerminalState(state string) bool {
	switch state {
	case "completed", "failed", "refused", "rejected", "interrupted", "cancelled":
		return true
	default:
		return false
	}
}
