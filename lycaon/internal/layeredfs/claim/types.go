// Package claim implements worker view claiming without lower-tree walks,
// Format-v2 persisted workspace metadata (with Format-v1 upgrade support),
// and the typed Host/Den workspace state surface (#1662).
package claim

import (
	"context"
	"errors"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/privategit"
)

const (
	// FormatVersionV1 identifies legacy materialized branch metadata (#1662).
	FormatVersionV1 = 1
	// FormatVersionV2 identifies layered/materialized workspace view metadata (#1662).
	FormatVersionV2 = 2
)

var (
	// ErrUnsupportedMetadataVersion is returned when persisted metadata has an unknown format_version.
	ErrUnsupportedMetadataVersion = errors.New("claim: unsupported workspace metadata format version")
	// ErrInvalidMetadata is returned when persisted metadata is malformed or missing required fields.
	ErrInvalidMetadata = errors.New("claim: invalid or incomplete workspace metadata")
	// ErrConfinementFailed is returned when mount or materialized workspace usability fails under confinement.
	ErrConfinementFailed = errors.New("claim: workspace confinement usability verification failed")
	// ErrRetentionFailed is returned when retention registration fails before workspace readiness.
	ErrRetentionFailed = errors.New("claim: workspace retention registration failed")
	// ErrViewNotFound is returned when querying or updating state for an unknown viewID.
	ErrViewNotFound = errors.New("claim: workspace view not found")
	// ErrSettlementUnresolved is returned when a view could not be settled. Its metadata, retention
	// and adapter state are kept, so the view stays in custody and the settlement can be retried.
	ErrSettlementUnresolved = errors.New("claim: workspace view settlement unresolved")
)

// ReadinessState reports the lifecycle readiness of a worker workspace view.
type ReadinessState string

const (
	StatePreparing         ReadinessState = "preparing"
	StateReady             ReadinessState = "ready"
	StateFailedPreparation ReadinessState = "failed_preparation"
	StateRecoveryRequired  ReadinessState = "recovery_required"
	StateReleased          ReadinessState = "released"
)

// CommandState reports background/foreground command execution independently of workspace readiness.
type CommandState string

const (
	CommandIdle      CommandState = "idle"
	CommandRunning   CommandState = "running"
	CommandCompleted CommandState = "completed"
	CommandFailed    CommandState = "failed"
)

// CheckpointState reports durable checkpoint publication status independently of workspace readiness.
type CheckpointState string

const (
	CheckpointNone              CheckpointState = "none"
	CheckpointPending           CheckpointState = "pending"
	CheckpointPublished         CheckpointState = "published"
	CheckpointFailedPersistence CheckpointState = "failed_persistence"
)

// GitInitState reports whether private Git metadata is pending lazy init, initialized, or failed.
type GitInitState string

const (
	GitInitLazyPending GitInitState = "lazy_pending"
	GitInitInitialized GitInitState = "initialized"
	GitInitFailed      GitInitState = "failed"
)

// NoticeSeverity classifies fallback and preparation notices for Host and Den presentation.
type NoticeSeverity string

const (
	SeverityNone    NoticeSeverity = ""
	SeverityInfo    NoticeSeverity = "info"
	SeverityWarning NoticeSeverity = "warning"
	SeverityError   NoticeSeverity = "error"
)

// RecoveryAction enumerates typed recovery/retry actions offered by the host to Den.
type RecoveryAction string

const (
	ActionRetryClaim             RecoveryAction = "retry_claim"
	ActionSelectSmallerWorkspace RecoveryAction = "select_smaller_workspace"
	ActionCancelClaim            RecoveryAction = "cancel_claim"
	ActionRecoverView            RecoveryAction = "recover_view"
	ActionRetryCheckpoint        RecoveryAction = "retry_checkpoint"
)

// ClaimStage identifies deterministic boundaries during worker view claim for cancellation and fault injection.
type ClaimStage string

const (
	StageValidateSelection ClaimStage = "validate_selection"
	StagePrepareAdapter    ClaimStage = "prepare_adapter"
	StageRegisterRetention ClaimStage = "register_retention"
	StageVerifyConfinement ClaimStage = "verify_confinement"
	StagePersistMetadata   ClaimStage = "persist_metadata"
)

// RetentionRegistrar registers and releases worker workspace retention claims (#1662, #1666).
type RetentionRegistrar interface {
	RegisterClaim(ctx context.Context, workerID, viewID string, rootPaths []string) (claimID string, err error)
	ReleaseClaim(claimID string) error
	// HoldUnresolved durably records that viewID failed settlement, until ReleaseClaim of its result.
	HoldUnresolved(ctx context.Context, workerID, viewID string) (claimID string, err error)
}

// ConfinementValidator verifies that a prepared mount or materialized tree is usable under confinement.
type ConfinementValidator func(ctx context.Context, view *adapter.PreparedView) error

// FaultHook injects deterministic faults or cancellations at named claim boundaries.
type FaultHook func(ctx context.Context, stage ClaimStage) error

// ClaimIdentity groups worker, project, view, and source generation identifiers.
type ClaimIdentity struct {
	WorkerID         string `json:"worker_id"`
	ProjectID        string `json:"project_id"`
	ViewID           string `json:"view_id"`
	SourceGeneration uint64 `json:"source_generation"`
}

// ClaimStorage configures runtime, backing, journal, and persisted metadata paths for a claim.
type ClaimStorage struct {
	RuntimeDir     string            `json:"runtime_dir"`
	BackingDir     string            `json:"backing_dir"`
	MetadataPath   string            `json:"metadata_path"`
	JournalPath    string            `json:"journal_path"`
	TargetRootDirs map[string]string `json:"target_root_dirs,omitempty"`
}

// ClaimMaterialization carries what preparing a materialized view needs beyond its storage.
type ClaimMaterialization struct {
	PristineDir      string
	StatManifestPath string
	CopyBudget       *adapter.CopyBudget
	GitObjectStore   *privategit.RetainedObjectStore
	CloneFile        adapter.FileCloner
	// Disk reads free space when this claim selects its own provider.
	Disk     func() (adapter.DiskSpace, error)
	Progress func(adapter.MaterializeProgress)
}

// ClaimHooks groups the retention, confinement, private-git, barrier and fault-injection services.
type ClaimHooks struct {
	Retention RetentionRegistrar
	// VerifyConfinement is required for a mounted view: nothing else establishes enforcement.
	VerifyConfinement ConfinementValidator
	PrivateGit        *privategit.Manager
	Quiesce           func(ctx context.Context, viewID string) error
	FlushClient       func(ctx context.Context, viewID string) error
	Observations      layeredfs.ObservationSink
	Fault             FaultHook
}

// ClaimRequest specifies a worker view claim across selected roots (#1662).
type ClaimRequest struct {
	Identity            ClaimIdentity
	Storage             ClaimStorage
	Grants              []adapter.RootGrant
	Selection           adapter.WorkspaceSelection
	FocusPathHints      []string
	ParentLayers        []layeredfs.ParentLayer
	ParentCheckpointIDs []string
	SelectionFacts      adapter.SelectionFacts
	Materialization     ClaimMaterialization
	Hooks               ClaimHooks
}

// ClaimedView holds the ready worker view, its selected adapter, persisted metadata, and state snapshot.
type ClaimedView struct {
	Identity         ClaimIdentity          `json:"identity"`
	Adapter          adapter.Adapter        `json:"-"`
	Prepared         *adapter.PreparedView  `json:"prepared"`
	Metadata         ViewMetadataV2         `json:"metadata"`
	RetentionClaimID string                 `json:"retention_claim_id,omitempty"`
	State            WorkspaceStateSnapshot `json:"state"`
	metaPath         string
	privateGit       *privategit.Manager
}
