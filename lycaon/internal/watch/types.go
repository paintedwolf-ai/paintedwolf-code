// Package watch persists watch subscriptions, matches, deliveries and wait selections.
package watch

import (
	"fmt"
	"strings"
	"time"

	awaitstore "github.com/lycaon/lycaon/internal/await"
)

const (
	ConditionWatchMatch  = "watch_match"
	ConditionProcessDone = "process_done"

	MaxLiveExplicitWatches = 8
	MaxTotalExplicitSlots  = 32
	MaxAlternatives        = 8
	MaxInlineMatchBytes    = 2048
	MaxBatchBytes          = 4096
	MaxBufferedMatches     = 32

	DefaultLifetime = 7 * 24 * time.Hour
	MinLifetime     = time.Minute
	MaxLifetime     = 30 * 24 * time.Hour

	DefaultHourlyWakeBudget = 12
)

type OwnerKind string

const (
	OwnerSession OwnerKind = "session"
	OwnerJob     OwnerKind = "job"
	OwnerPerson  OwnerKind = "person"
)

type DeliveryMode string

const (
	DeliveryCollect DeliveryMode = "collect"
	DeliveryWake    DeliveryMode = "wake"
	DeliveryRun     DeliveryMode = "run"
)

type State string

const (
	StateLive      State = "live"
	StateTerminal  State = "terminal"
	StateEnabled   State = "enabled"
	StatePaused    State = "paused"
	StateDisabled  State = "disabled"
	StateCompleted State = "completed"
)

type TerminalOutcome string

const (
	OutcomeCompleted  TerminalOutcome = "completed"
	OutcomeExpired    TerminalOutcome = "expired"
	OutcomeCanceled   TerminalOutcome = "canceled"
	OutcomeSourceLost TerminalOutcome = "source_lost"
	OutcomeKept       TerminalOutcome = "kept"
	OutcomeDismissed  TerminalOutcome = "dismissed"
)

type DestinationKind string

const (
	DestinationRequest      DestinationKind = "request"
	DestinationLease        DestinationKind = "lease"
	DestinationFinalReceipt DestinationKind = "final_receipt"
	DestinationAmbientWake  DestinationKind = "ambient_wake"
)

// Owner identifies who owns a watch registration.
type Owner struct {
	Kind          OwnerKind `json:"kind"`
	ProjectID     string    `json:"project_id"`
	SessionID     string    `json:"session_id,omitempty"`
	WorkerJobID   string    `json:"worker_job_id,omitempty"`
	OwnerPersonID string    `json:"owner_person_id,omitempty"`
}

func (o Owner) Normalized() Owner {
	o.ProjectID = strings.TrimSpace(o.ProjectID)
	o.SessionID = strings.TrimSpace(o.SessionID)
	o.WorkerJobID = strings.TrimSpace(o.WorkerJobID)
	o.OwnerPersonID = strings.TrimSpace(o.OwnerPersonID)
	switch {
	case o.Kind == OwnerPerson || (o.OwnerPersonID != "" && o.SessionID == ""):
		o.Kind = OwnerPerson
	case o.WorkerJobID != "":
		o.Kind = OwnerJob
	default:
		o.Kind = OwnerSession
	}
	return o
}

func (o Owner) Destination() string {
	if o.Kind == OwnerPerson {
		return "run"
	}
	if strings.TrimSpace(o.WorkerJobID) != "" {
		return "job"
	}
	return "session"
}

// WatchRootGrant captures one authorized root mapping frozen at registration.
type WatchRootGrant struct {
	Label         string `json:"label"`
	RootID        string `json:"root_id,omitempty"`
	Path          string `json:"path"`
	BranchID      string `json:"branch_id,omitempty"`
	PrivateBranch bool   `json:"private_branch,omitempty"`
}

// Authority captures workspace and capability scope frozen at registration.
type Authority struct {
	RootSessionID   string           `json:"root_session_id,omitempty"`
	ProjectDir      string           `json:"project_dir,omitempty"`
	WorkspaceID     string           `json:"workspace_id,omitempty"`
	RootPaths       []string         `json:"root_paths,omitempty"`
	Roots           []WatchRootGrant `json:"roots,omitempty"`
	LoopbackGranted bool             `json:"loopback_granted,omitempty"`
	LoopbackPorts   []uint16         `json:"loopback_ports,omitempty"`
}

// StandingDraft captures an agent-proposed standing watch attached to a session watch.
type StandingDraft struct {
	Name     string `json:"name"`
	Prompt   string `json:"prompt"`
	Replaces string `json:"replaces,omitempty"`
}

// OverlappingStandingWatch summarizes an existing project standing watch with overlapping sources.
type OverlappingStandingWatch struct {
	WatchID string `json:"watch_id"`
	Name    string `json:"name"`
	State   string `json:"state"`
}

// RegisterRequest creates or idempotently replays a watch registration.
type RegisterRequest struct {
	Owner                Owner
	InvocationID         string
	DerivedCommandHandle string
	Conditions           []awaitstore.Condition
	Authority            Authority
	Cursors              map[string]string
	Delivery             DeliveryMode
	Repeat               bool
	ExpiresAt            time.Time
	Standing             *StandingDraft
}

// RegisterReceipt is the durable result returned when a watch is registered.
type RegisterReceipt struct {
	WatchID             string                     `json:"watch_id"`
	Handle              string                     `json:"handle"`
	State               string                     `json:"state"`
	Delivery            DeliveryMode               `json:"delivery"`
	Destination         string                     `json:"destination"`
	ExpiresAt           string                     `json:"expires_at"`
	WaitCondition       awaitstore.Condition       `json:"wait_condition"`
	PendingMatches      int                        `json:"pending_matches"`
	Hint                string                     `json:"hint,omitempty"`
	StandingDraft       bool                       `json:"standing_draft,omitempty"`
	OverlappingStanding []OverlappingStandingWatch `json:"overlapping_standing,omitempty"`
}

// OverflowSummary preserves aggregate match counts and state transitions across compaction.
type OverflowSummary struct {
	CompactedCount int      `json:"compacted_count"`
	FirstSeq       int      `json:"first_seq"`
	LastSeq        int      `json:"last_seq"`
	Outcomes       []string `json:"outcomes"`
	HadFailure     bool     `json:"had_failure,omitempty"`
	HadRecovery    bool     `json:"had_recovery,omitempty"`
	LatestOutcome  string   `json:"latest_outcome,omitempty"`
}

// Registration is a stored watch record.
type Registration struct {
	WatchID              string
	Owner                Owner
	Handle               string
	Seq                  int
	DerivedCommandHandle string
	InvocationID         string
	Standing             *StandingDraft
	Conditions           []awaitstore.Condition
	Authority            Authority
	Cursors              map[string]string
	Lifecycle            Lifecycle
	CreatedAt            time.Time
	UpdatedAt            time.Time
	EndedAt              *time.Time
}

// Lifecycle groups mutable watch state and retention metadata.
type Lifecycle struct {
	Delivery       DeliveryMode
	Repeat         bool
	ExpiresAt      time.Time
	State          State
	StateReason    string
	Outcome        TerminalOutcome
	SlotReleased   bool
	MatchSeq       int
	OverflowCount  int
	Overflow       *OverflowSummary
	FinalReceiptID string
	Revision       int
}

// CapacityError reports an owner at the explicit watch limit.
type CapacityError struct {
	LiveCount          int
	SlotCount          int
	ReclaimableHandles []string
}

func (e *CapacityError) Error() string {
	return fmt.Sprintf("watch capacity reached (live=%d, slots=%d)", e.LiveCount, e.SlotCount)
}

// ValidationError identifies a rejected watch request code and invalid field.
type ValidationError struct {
	Code    string
	Field   string
	Fields  []string
	Message string
}

func (e *ValidationError) InvalidFields() []string {
	if len(e.Fields) > 0 {
		return append([]string(nil), e.Fields...)
	}
	if e.Field != "" {
		return []string{e.Field}
	}
	return nil
}

func (e *ValidationError) Error() string {
	if e.Field != "" {
		return fmt.Sprintf("%s (%s): %s", e.Code, e.Field, e.Message)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// UnknownHandleError reports an unowned or missing watch handle.
type UnknownHandleError struct {
	Handle string
}

func (e *UnknownHandleError) Error() string {
	return fmt.Sprintf("unknown watch handle %q", e.Handle)
}

// TerminalError reports a mutation attempted on a ended watch.
type TerminalError struct {
	Handle  string
	Outcome TerminalOutcome
}

func (e *TerminalError) Error() string {
	return fmt.Sprintf("watch %s is terminal (%s)", e.Handle, e.Outcome)
}
