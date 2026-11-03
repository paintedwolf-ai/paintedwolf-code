// Package recovery implements worker view crash recovery, cancellation,
// retention, garbage collection, and stacked checkpoints (#1666).
package recovery

import (
	"errors"
	"fmt"
	"time"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
)

// FormatVersion is the schema version for persisted recovery and retention metadata.
const FormatVersion = 1

var (
	ErrUnsupportedFormatVersion  = errors.New("recovery: unsupported metadata format version")
	ErrCorruptRecoveryMetadata   = errors.New("recovery: corrupt or inconsistent recovery metadata")
	ErrWorkerIdentityMismatch    = errors.New("recovery: worker identity or ownership mismatch")
	ErrIncompleteCheckpoint      = errors.New("recovery: checkpoint is incomplete or missing required objects")
	ErrMissingRetainedObject     = errors.New("recovery: required blob or git object is missing from retention")
	ErrUnresolvedSettlement      = errors.New("recovery: worker processes or mounts did not settle within bounds")
	ErrEvictUnsealedWorkRefused  = errors.New("recovery: cannot evict working material with unsealed mutations")
	ErrEvictReferencedCheckpoint = errors.New("recovery: cannot evict checkpoint referenced by active claims or stacked children")
	ErrParentCheckpointNotFound  = errors.New("recovery: parent checkpoint not found or not reconstructible")
	ErrParentMutationForbidden   = errors.New("recovery: immutable parent checkpoint cannot be mutated")
	ErrNoValidCheckpoint         = errors.New("recovery: no valid published checkpoint available for recovery")
	ErrActiveUnsealedWork        = errors.New("recovery: view has unsealed working mutations that cannot be discarded")
)

// UnresolvedSettlementError reports a bounded cleanup or settlement failure while preserving custody.
type UnresolvedSettlementError struct {
	WorkerID            string
	ViewID              string
	SurvivingPIDs       []int
	MountBusy           bool
	BusyMountPaths      []string
	Reason              string
	RetainedCustody     bool
	RetainedStorageRefs []string
	Cause               error
}

func (e *UnresolvedSettlementError) Error() string {
	return fmt.Sprintf(
		"%v: worker=%s view=%s pids=%v mount_busy=%t retained_custody=%t: %s",
		ErrUnresolvedSettlement, e.WorkerID, e.ViewID, e.SurvivingPIDs, e.MountBusy, e.RetainedCustody, e.Reason,
	)
}

func (e *UnresolvedSettlementError) Unwrap() error {
	if e.Cause != nil {
		return errors.Join(ErrUnresolvedSettlement, e.Cause)
	}
	return ErrUnresolvedSettlement
}

// TransitionPhase names a durable lifecycle phase in the worker transition journal.
type TransitionPhase string

const (
	PhasePreparation    TransitionPhase = "preparation"
	PhaseActiveMutation TransitionPhase = "active_mutation"
	PhaseBarrierFlush   TransitionPhase = "barrier_flush"
	PhasePublication    TransitionPhase = "publication"
	PhaseRefImport      TransitionPhase = "ref_import"
	PhaseFilePromotion  TransitionPhase = "file_promotion"
	PhaseRelease        TransitionPhase = "release"
)

// TransitionState tracks whether a journaled lifecycle transition completed or was interrupted.
type TransitionState string

const (
	StateBegin       TransitionState = "begin"
	StateCommitted   TransitionState = "committed"
	StateInterrupted TransitionState = "interrupted"
	StateFailed      TransitionState = "failed"
)

// LifecycleTransitionRecord records one durable phase transition in a worker's lifecycle.
type LifecycleTransitionRecord struct {
	Seq          uint64                    `json:"seq"`
	Phase        TransitionPhase           `json:"phase"`
	State        TransitionState           `json:"state"`
	ViewID       string                    `json:"view_id"`
	CheckpointID string                    `json:"checkpoint_id,omitempty"`
	Operation    string                    `json:"operation,omitempty"`
	Paths        []layeredfs.QualifiedPath `json:"paths,omitempty"`
	UnknownScope bool                      `json:"unknown_scope,omitempty"`
	Diagnostic   string                    `json:"diagnostic,omitempty"`
	RecordedAt   time.Time                 `json:"recorded_at"`
}

// LandingReceipt records explicit, verified ancestry/landing facts when a checkpoint promotes to trunk.
type LandingReceipt struct {
	FormatVersion     int                                     `json:"format_version"`
	CheckpointID      string                                  `json:"checkpoint_id"`
	TransactionID     string                                  `json:"transaction_id"`
	WorkerID          string                                  `json:"worker_id"`
	TaskID            string                                  `json:"task_id,omitempty"`
	ProjectID         string                                  `json:"project_id"`
	ParentCheckpoints []string                                `json:"parent_checkpoints,omitempty"`
	PromotedDigests   map[string]checkpoint.TargetObservation `json:"promoted_digests"`
	ImportedRefs      map[string]string                       `json:"imported_refs,omitempty"`
	LandedAt          time.Time                               `json:"landed_at"`
}

// WorkerLifecycleJournal persists durable state across preparation, publication, promotion, and release.
type WorkerLifecycleJournal struct {
	FormatVersion             int                          `json:"format_version"`
	WorkerID                  string                       `json:"worker_id"`
	ViewID                    string                       `json:"view_id"`
	TaskID                    string                       `json:"task_id"`
	ProjectID                 string                       `json:"project_id"`
	AdapterID                 adapter.AdapterID            `json:"adapter_id"`
	BootID                    string                       `json:"boot_id"`
	ParentCheckpointIDs       []string                     `json:"parent_checkpoint_ids,omitempty"`
	LastPublishedCheckpointID string                       `json:"last_published_checkpoint_id,omitempty"`
	ActivePhase               TransitionPhase              `json:"active_phase,omitempty"`
	ActiveOperation           string                       `json:"active_operation,omitempty"`
	ActivePaths               []layeredfs.QualifiedPath    `json:"active_paths,omitempty"`
	ActiveUnknownScope        bool                         `json:"active_unknown_scope,omitempty"`
	GitRetentionClaims        []string                     `json:"git_retention_claims,omitempty"`
	SourceObservations        []checkpoint.SourceObservation `json:"source_observations,omitempty"`
	Transitions               []LifecycleTransitionRecord  `json:"transitions,omitempty"`
	UpdatedAt                 time.Time                    `json:"updated_at"`
}

// InventoryBucket classifies local storage entries by durability and retention role.
type InventoryBucket string

const (
	BucketRetainedWorkerResults  InventoryBucket = "retained_worker_results"
	BucketInterruptedWorkingData InventoryBucket = "interrupted_working_data"
	BucketDisposableRuntimeCache InventoryBucket = "disposable_runtime_cache"
)

// InventoryEntry represents one tracked entry in the local-data inventory.
type InventoryEntry struct {
	ID           string          `json:"id"`
	Bucket       InventoryBucket `json:"bucket"`
	WorkerID     string          `json:"worker_id"`
	ViewID       string          `json:"view_id,omitempty"`
	CheckpointID string          `json:"checkpoint_id,omitempty"`
	Path         string          `json:"path"`
	Bytes        int64           `json:"bytes"`
	PinnedBy     []string        `json:"pinned_by,omitempty"`
	UpdatedAt    time.Time       `json:"updated_at"`
}

// LocalDataInventory persists the categorized local-data buckets.
type LocalDataInventory struct {
	FormatVersion int              `json:"format_version"`
	Entries       []InventoryEntry `json:"entries"`
	UpdatedAt     time.Time        `json:"updated_at"`
}

// InterruptedWorkingRecord describes preserved incomplete working storage salvaged during recovery/cancel.
type InterruptedWorkingRecord struct {
	FormatVersion    int                       `json:"format_version"`
	ID               string                    `json:"id"`
	WorkerID         string                    `json:"worker_id"`
	ViewID           string                    `json:"view_id"`
	BaseCheckpointID string                    `json:"base_checkpoint_id,omitempty"`
	QuarantineDir    string                    `json:"quarantine_dir"`
	InterruptedPhase TransitionPhase           `json:"interrupted_phase"`
	InterruptedOp    string                    `json:"interrupted_op,omitempty"`
	AffectedPaths    []layeredfs.QualifiedPath `json:"affected_paths,omitempty"`
	UnknownScope     bool                      `json:"unknown_scope"`
	Diagnostic       string                    `json:"diagnostic,omitempty"`
	CreatedAt        time.Time                 `json:"created_at"`
}

// MountIdentityRecord binds runtime mount/process facts to a specific worker view.
type MountIdentityRecord struct {
	FormatVersion  int               `json:"format_version"`
	ViewID         string            `json:"view_id"`
	WorkerID       string            `json:"worker_id"`
	AdapterID      adapter.AdapterID `json:"adapter_id"`
	MountID        string            `json:"mount_id,omitempty"`
	ConnectionID   string            `json:"connection_id,omitempty"`
	ExportSource   string            `json:"export_source,omitempty"`
	MountPaths     []string          `json:"mount_paths,omitempty"`
	RuntimeViewDir string            `json:"runtime_view_dir"`
	BackingDir     string            `json:"backing_dir"`
	BootID         string            `json:"boot_id"`
	RecordedPIDs   []int             `json:"recorded_pids,omitempty"`
	UpdatedAt      time.Time         `json:"updated_at"`
}

// ProcessSettlementReceipt reports the result of stopping a worker's recorded process lineage.
type ProcessSettlementReceipt struct {
	WorkerID      string
	ViewID        string
	StoppedPIDs   []int
	SurvivingPIDs []int
	Settled       bool
}

// ProcessController supervises and settles processes belonging exclusively to a specific worker lineage.
type ProcessController interface {
	SettleProcesses(workerID, viewID string, recordedPIDs []int) (ProcessSettlementReceipt, error)
}

// MountSettlementReceipt reports reconciliation of a specific worker view's mounts and connections.
type MountSettlementReceipt struct {
	ViewID            string
	MountID           string
	UnmountedPaths    []string
	BusyPaths         []string
	ConnectionAborted bool
	Settled           bool
}

// MountReconciler settles or aborts a specific worker view's mount/connection by recorded identity.
type MountReconciler interface {
	ReconcileWorkerMount(rec MountIdentityRecord) (MountSettlementReceipt, error)
}

// FaultInjector injects deterministic faults at named recovery/lifecycle phase boundaries.
type FaultInjector func(phase TransitionPhase, step string) error
