// Package watchdiag implements opt-in, bounded development runtime diagnostics
// for wait and watch delivery, presentation claims, report-publication fencing,
// and session-tree Stop fences. It defaults off, observes immutable typed
// snapshots asynchronously on a bounded queue, never reads prose, never blocks
// or alters product execution, and retains only redacted metadata and owning
// source references.
package watchdiag

import (
	"context"
	"time"
)

// SchemaVersion is the wire schema version for emitted watch diagnostic records.
const SchemaVersion = 1

// PhaseProcessReportPublished is the authoritative phase record emitted when a
// background or held process completion report has been published.
const PhaseProcessReportPublished = "process_report_published"

// Disposition classifies each emitted diagnostic finding.
type Disposition string

const (
	DispositionContradiction Disposition = "contradiction"
	DispositionIncomplete    Disposition = "incomplete"
	DispositionUnsupported   Disposition = "unsupported"
	DispositionOverflow      Disposition = "overflow"
)

// RuleID identifies one bounded wait/watch diagnostic check.
type RuleID string

const (
	RuleWrongRecipient                 RuleID = "wrong_recipient"
	RuleDuplicateCommittedDelivery     RuleID = "duplicate_committed_delivery"
	RuleDuplicateCommittedPresentation RuleID = "duplicate_committed_presentation"
	RuleTerminalDeliveryBeforeReport   RuleID = "terminal_delivery_before_report"
	RuleWatchTurnPastStopFence         RuleID = "watch_turn_past_stop_fence"
	RuleCaptureOverflow                RuleID = "capture_overflow"
	RuleObservationLoss                RuleID = "observation_loss"
)

// ClockDomain identifies which clock produced a diagnostic input's timestamps.
type ClockDomain string

const (
	ClockDomainSystem ClockDomain = "system"
	ClockDomainManual ClockDomain = "manual"
)

// Default bounds for async capture, local report retention, and emitted metadata.
const (
	DefaultQueueCapacity    = 64
	DefaultReportCapacity   = 256
	DefaultMaxIDsPerFinding = 16
	DefaultMaxFieldBytes    = 256
	DefaultTaskTimeout      = 5 * time.Second
	DefaultCloseGrace       = 250 * time.Millisecond
	DefaultMaxDedupEntries  = 4096
)

// OwningSource identifies the component, authoritative phase, and clock domain
// that produced a diagnostic input record.
type OwningSource struct {
	Component   string      `json:"component,omitempty"`
	Phase       string      `json:"phase,omitempty"`
	ClockDomain ClockDomain `json:"clock_domain,omitempty"`
	EvidenceRef string      `json:"evidence_ref,omitempty"`
}

// SourceRef binds a diagnostic finding to its structured durable identities and
// owning source metadata.
type SourceRef struct {
	SessionID    string       `json:"session_id,omitempty"`
	WorkerJobID  string       `json:"worker_job_id,omitempty"`
	WatchID      string       `json:"watch_id,omitempty"`
	Handle       string       `json:"handle,omitempty"`
	OccurrenceID string       `json:"occurrence_id,omitempty"`
	ClaimID      string       `json:"claim_id,omitempty"`
	TurnID       string       `json:"turn_id,omitempty"`
	StopEpoch    int64        `json:"stop_epoch,omitempty"`
	EvidenceRef  string       `json:"evidence_ref,omitempty"`
	OwningSource OwningSource `json:"owning_source,omitempty"`
}

// Task is a lightweight bounded work item enqueued onto the async worker before
// any store reads occur. When the queue is saturated, Fetch is never invoked.
type Task struct {
	SessionID string
	WatchID   string
	Handle    string
	Fetch     func(ctx context.Context) (Observation, error)
}

// Observation is an immutable, metadata-only snapshot of typed wait/watch facts.
// It deliberately excludes message prose, command output, prompts, and file
// contents; all checks read typed records and identities only.
type Observation struct {
	SessionID         string                 `json:"session_id,omitempty"`
	WorkerJobID       string                 `json:"worker_job_id,omitempty"`
	WatchID           string                 `json:"watch_id,omitempty"`
	Handle            string                 `json:"handle,omitempty"`
	ClockDomain       ClockDomain            `json:"clock_domain,omitempty"`
	OwningSource      OwningSource           `json:"owning_source,omitempty"`
	RetentionGap      bool                   `json:"retention_gap,omitempty"`
	LagPending        bool                   `json:"lag_pending,omitempty"`
	CoalescedCount    int                    `json:"coalesced_count,omitempty"`
	ReconnectRepaint  bool                   `json:"reconnect_repaint,omitempty"`
	Truncated         bool                   `json:"truncated,omitempty"`
	IncompleteReason  string                 `json:"incomplete_reason,omitempty"`
	Deliveries        []DeliveryFact         `json:"deliveries,omitempty"`
	Presentations     []PresentationFact     `json:"presentations,omitempty"`
	PublicationPhases []PublicationPhaseFact `json:"publication_phases,omitempty"`
	StopFence         *StopFenceFact         `json:"stop_fence,omitempty"`
	AdmittedTurns     []WatchTurnFact        `json:"admitted_turns,omitempty"`
}

// DeliveryFact records one typed wait/watch delivery routing and commit fact.
type DeliveryFact struct {
	DeliveryID        string       `json:"delivery_id,omitempty"`
	WatchID           string       `json:"watch_id,omitempty"`
	Handle            string       `json:"handle,omitempty"`
	OccurrenceID      string       `json:"occurrence_id,omitempty"`
	OwnerSessionID    string       `json:"owner_session_id,omitempty"`
	OwnerWorkerJobID  string       `json:"owner_worker_job_id,omitempty"`
	RoutedSessionID   string       `json:"routed_session_id,omitempty"`
	RoutedWorkerJobID string       `json:"routed_worker_job_id,omitempty"`
	Terminal          bool         `json:"terminal,omitempty"`
	Committed         bool         `json:"committed,omitempty"`
	RequiresReport    bool         `json:"requires_report,omitempty"`
	CommittedAt       time.Time    `json:"committed_at,omitempty"`
	CommittedSeq      int64        `json:"committed_seq,omitempty"`
	ClockDomain       ClockDomain  `json:"clock_domain,omitempty"`
	OwningSource      OwningSource `json:"owning_source,omitempty"`
	EvidenceRef       string       `json:"evidence_ref,omitempty"`
}

// PresentationFact records one typed presentation claim or replay event.
type PresentationFact struct {
	ClaimID             string       `json:"claim_id,omitempty"`
	WatchID             string       `json:"watch_id,omitempty"`
	Handle              string       `json:"handle,omitempty"`
	OccurrenceID        string       `json:"occurrence_id,omitempty"`
	OwnerSessionID      string       `json:"owner_session_id,omitempty"`
	OwnerWorkerJobID    string       `json:"owner_worker_job_id,omitempty"`
	RoutedSessionID     string       `json:"routed_session_id,omitempty"`
	RoutedWorkerJobID   string       `json:"routed_worker_job_id,omitempty"`
	Kind                string       `json:"kind,omitempty"`
	Committed           bool         `json:"committed,omitempty"`
	IsHistoryReread     bool         `json:"is_history_reread,omitempty"`
	IsReconnectRepaint  bool         `json:"is_reconnect_repaint,omitempty"`
	IsLateReceiptLookup bool         `json:"is_late_receipt_lookup,omitempty"`
	CoalescedFrom       []string     `json:"coalesced_from,omitempty"`
	TurnID              string       `json:"turn_id,omitempty"`
	ClockDomain         ClockDomain  `json:"clock_domain,omitempty"`
	OwningSource        OwningSource `json:"owning_source,omitempty"`
	EvidenceRef         string       `json:"evidence_ref,omitempty"`
}

// PublicationPhaseFact records an authoritative report publication phase record
// (such as process_report_published) with its owning source and clock domain.
type PublicationPhaseFact struct {
	SessionID    string       `json:"session_id,omitempty"`
	Handle       string       `json:"handle,omitempty"`
	OccurrenceID string       `json:"occurrence_id,omitempty"`
	Phase        string       `json:"phase,omitempty"`
	RecordedAt   time.Time    `json:"recorded_at,omitempty"`
	RecordedSeq  int64        `json:"recorded_seq,omitempty"`
	ClockDomain  ClockDomain  `json:"clock_domain,omitempty"`
	OwningSource OwningSource `json:"owning_source,omitempty"`
	EvidenceRef  string       `json:"evidence_ref,omitempty"`
}

// StopFenceFact records a durable session-tree Stop fence epoch and timestamp.
type StopFenceFact struct {
	SessionID     string       `json:"session_id,omitempty"`
	RootSessionID string       `json:"root_session_id,omitempty"`
	StopEpoch     int64        `json:"stop_epoch,omitempty"`
	StoppedAt     time.Time    `json:"stopped_at,omitempty"`
	ClockDomain   ClockDomain  `json:"clock_domain,omitempty"`
	OwningSource  OwningSource `json:"owning_source,omitempty"`
	EvidenceRef   string       `json:"evidence_ref,omitempty"`
}

// WatchTurnFact records one turn admission fact for Stop-fence checking.
type WatchTurnFact struct {
	TurnID          string       `json:"turn_id,omitempty"`
	SessionID       string       `json:"session_id,omitempty"`
	WatchID         string       `json:"watch_id,omitempty"`
	Handle          string       `json:"handle,omitempty"`
	OccurrenceID    string       `json:"occurrence_id,omitempty"`
	ClaimID         string       `json:"claim_id,omitempty"`
	WatchOriginated bool         `json:"watch_originated,omitempty"`
	Admitted        bool         `json:"admitted,omitempty"`
	StopEpoch       int64        `json:"stop_epoch,omitempty"`
	AdmittedAt      time.Time    `json:"admitted_at,omitempty"`
	ClockDomain     ClockDomain  `json:"clock_domain,omitempty"`
	OwningSource    OwningSource `json:"owning_source,omitempty"`
	EvidenceRef     string       `json:"evidence_ref,omitempty"`
}

// Finding is one structured, self-identifying diagnostic emitted per rule and
// entity (or for capture overflow).
type Finding struct {
	SchemaVersion int            `json:"schema_version"`
	Timestamp     time.Time      `json:"ts"`
	RuleID        RuleID         `json:"rule_id"`
	Disposition   Disposition    `json:"disposition"`
	SessionID     string         `json:"session_id,omitempty"`
	WorkerJobID   string         `json:"worker_job_id,omitempty"`
	WatchID       string         `json:"watch_id,omitempty"`
	Handle        string         `json:"handle,omitempty"`
	OccurrenceID  string         `json:"occurrence_id,omitempty"`
	ClaimID       string         `json:"claim_id,omitempty"`
	TurnID        string         `json:"turn_id,omitempty"`
	StopEpoch     int64          `json:"stop_epoch,omitempty"`
	EvidenceRef   string         `json:"evidence_ref,omitempty"`
	OwningSource  OwningSource   `json:"owning_source,omitempty"`
	Sources       []SourceRef    `json:"sources,omitempty"`
	Expected      map[string]any `json:"expected,omitempty"`
	Observed      map[string]any `json:"observed,omitempty"`
	Reason        string         `json:"reason,omitempty"`
	DroppedCount  int64          `json:"dropped_count,omitempty"`
}

// LocalReport is a bounded, deduplicated snapshot of retained local findings.
type LocalReport struct {
	SchemaVersion     int       `json:"schema_version"`
	GeneratedAt       time.Time `json:"generated_at,omitempty"`
	Capacity          int       `json:"capacity"`
	TotalFindings     int       `json:"total_findings"`
	DeduplicatedCount int64     `json:"deduplicated_count"`
	DroppedCount      int64     `json:"dropped_count"`
	EvictedCount      int64     `json:"evicted_count"`
	Findings          []Finding `json:"findings,omitempty"`
}
