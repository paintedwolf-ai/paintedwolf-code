// Package privategit implements lazy private worker Git initialization,
// the engine-retained content-addressed object store, write-leg repository
// operations, and checkpointed idempotent namespaced ref import (#1671).
package privategit

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/lycaon/lycaon/internal/layeredfs"
)

var (
	// ErrUnauthorizedGitTarget is returned when a gitlink, commondir, or alternate
	// points outside the authorized workspace/repository roots.
	ErrUnauthorizedGitTarget = fmt.Errorf("%w: unauthorized git metadata target", layeredfs.ErrPathEscape)

	// ErrNativeToolGitMutationForbidden is returned when a native file-editing tool
	// attempts to directly mutate .git metadata paths.
	ErrNativeToolGitMutationForbidden = fmt.Errorf("%w: direct native file-tool mutation of .git is forbidden", fs.ErrPermission)

	// ErrRemoteOperationRefused is returned when fetch or push is denied by RemotePolicy.
	ErrRemoteOperationRefused = errors.New("privategit: remote operation refused by egress/approval policy")

	// ErrRefImportConflict is returned when a destination ref in trunk already exists
	// with different recorded content or conflicts with a recorded import journal.
	ErrRefImportConflict = errors.New("privategit: destination ref conflict during trunk import")

	// ErrIdentityMismatch is returned when a ref import request does not match the
	// worker or checkpoint identity recorded in the checkpoint state.
	ErrIdentityMismatch = errors.New("privategit: worker or checkpoint identity mismatch")

	// ErrSeedLimitExceeded is returned when materialized fallback seeding exceeds
	// configured file count or byte limits.
	ErrSeedLimitExceeded = errors.New("privategit: materialized seed limit exceeded")

	// ErrCorruptObjectStore is returned when imported Git objects fail integrity or
	// connectivity verification.
	ErrCorruptObjectStore = errors.New("privategit: object integrity or connectivity check failed")

	// ErrMountedWorktreeUnavailable is returned when a mounted view has no bound
	// host-owned FUSE/NFS mounted workspace path or when the mounted path is not active.
	ErrMountedWorktreeUnavailable = errors.New("privategit: mounted workspace worktree path is unavailable or not mounted")
)

// LeaseKind classifies an active object retention lease in the engine-retained store.
type LeaseKind string

const (
	LeaseView         LeaseKind = "view"
	LeaseCheckpoint   LeaseKind = "checkpoint"
	LeaseStackedChild LeaseKind = "stacked_child"
	LeaseRefImport    LeaseKind = "ref_import"
)

// StoreMetrics reports cumulative object and byte accounting for the retained store.
// RetainedObjects is the number of unique logical Git object OIDs retained in the store
// (deduplicated across loose shards and packfiles). RetainedBytes is the total on-disk
// byte count stored in objects/ across loose object files and pack/index files (.pack + .idx).
// StartupInitDuration records the wall-clock duration spent reconstructing stock on store open/reopen.
type StoreMetrics struct {
	ObjectsImported     int64         `json:"objects_imported"`
	ObjectsReused       int64         `json:"objects_reused"`
	BytesImported       int64         `json:"bytes_imported"`
	BytesReused         int64         `json:"bytes_reused"`
	BytesNewlyWritten   int64         `json:"bytes_newly_written"`
	RetainedObjects     int64         `json:"retained_objects"`
	RetainedBytes       int64         `json:"retained_bytes"`
	StartupInitDuration time.Duration `json:"startup_init_duration"`
}

// StockInventory reports an explicit, cancellable qualification inventory of the retained
// object store on disk outside the routine O(1) Metrics() hot path (#1671).
type StockInventory struct {
	LogicalObjects int64 `json:"logical_objects"`
	LooseObjects   int64 `json:"loose_objects"`
	PackedObjects  int64 `json:"packed_objects"`
	StoredBytes    int64 `json:"stored_bytes"`
	LooseBytes     int64 `json:"loose_bytes"`
	PackBytes      int64 `json:"pack_bytes"`
	Complete       bool  `json:"complete"`
}

// InitState represents the lifecycle state of a private repository in a view.
type InitState string

const (
	StateUninitialized InitState = "uninitialized"
	StatePending       InitState = "pending"
	StateReady         InitState = "ready"
	StateFailed        InitState = "failed"
)

// InitMetrics records first-use or seed-time repository initialization cost.
type InitMetrics struct {
	State           InitState     `json:"state"`
	ObjectsImported int64         `json:"objects_imported"`
	ObjectsReused   int64         `json:"objects_reused"`
	BytesImported   int64         `json:"bytes_imported"`
	BytesReused     int64         `json:"bytes_reused"`
	Duration        time.Duration `json:"duration"`
	Attempts        int           `json:"attempts"`
}

// AccessOrigin distinguishes OS/shell filesystem operations from native file tools.
type AccessOrigin string

const (
	OriginFilesystem AccessOrigin = "filesystem"
	OriginNativeTool AccessOrigin = "native_tool"
)

// RemotePolicy governs egress, remote approval, and credential checks for fetch/push.
type RemotePolicy struct {
	AllowFetch      bool
	AllowPush       bool
	ApprovedRemotes map[string]bool
	CredentialCheck func(ctx context.Context, op, remote, targetURL string) error
}

// MountedViewBinding records the adapter/supervisor-owned mount identity and lifecycle
// facts that bind a worker view, root, and layeredfs.Core to a mounted workspace path (#1671).
type MountedViewBinding struct {
	ViewID string
	// CoreViewID names the view whose Core serves a non-primary root; empty means ViewID.
	CoreViewID   string
	WorkerID     string
	RootID       string
	TrunkDir     string
	MountPath    string
	AdapterID    string
	Core         *layeredfs.Core
	MountID      string
	ConnectionID string
	Active       bool
	IsActive     func() bool
}

// SeedLimits bounds materialized fallback workspace seeding.
type SeedLimits struct {
	MaxEntries int64
	MaxBytes   int64
}

// MaterializedSeedConfig configures materialized fallback seeding and private Git preparation.
type MaterializedSeedConfig struct {
	ViewID            string
	WorkerID          string
	TrunkDir          string
	TargetDir         string
	Core              *layeredfs.Core
	AuthorizedGitDirs []string
	Limits            SeedLimits
}

// MaterializedSeedResult reports the outcome of materialized fallback seeding.
type MaterializedSeedResult struct {
	EntriesSeeded int64
	BytesSeeded   int64
	Repositories  []string
	RepoMetrics   map[string]InitMetrics
}

// SeededRepo names private metadata a materialized seed built for one repository in its worktree.
type SeededRepo struct {
	RepoRel       string
	PrivateGitDir string
	Metrics       InitMetrics
}

// MaterializedViewBinding hands a seeded materialized worktree to this manager.
type MaterializedViewBinding struct {
	ViewID      string
	WorkerID    string
	RootID      string
	TrunkDir    string
	WorktreeDir string
	Core        *layeredfs.Core
	Repos       []SeededRepo
}

// StashEntry captures one recoverable worker stash entry at checkpoint time.
type StashEntry struct {
	Ref     string `json:"ref"`
	OID     string `json:"oid"`
	Message string `json:"message"`
}

// CheckpointGitState holds the immutable checkpointed Git state for a worker view.
type CheckpointGitState struct {
	ViewID       string                 `json:"view_id"`
	WorkerID     string                 `json:"worker_id"`
	CheckpointID string                 `json:"checkpoint_id"`
	HeadSymbolic string                 `json:"head_symbolic"`
	HeadOID      string                 `json:"head_oid"`
	DetachedHead bool                   `json:"detached_head"`
	Refs         map[string]string      `json:"refs"`
	InitialRefs  map[string]string      `json:"initial_refs"`
	DeletedRefs  []string               `json:"deleted_refs"`
	Tags         map[string]string      `json:"tags"`
	Stashes      []StashEntry           `json:"stashes"`
	Artifact     CheckpointGitArtifacts `json:"artifact"`
}

// CheckpointGitArtifacts holds the binary index/config payload and object lease for a checkpoint.
type CheckpointGitArtifacts struct {
	IndexBytes      []byte            `json:"index_bytes"`
	ConfigBytes     []byte            `json:"config_bytes"`
	LeaseID         string            `json:"lease_id"`
	ReachableOIDs   []string          `json:"reachable_oids"`
	ImportedObjects int64             `json:"imported_objects"`
	SubmoduleHeads  map[string]string `json:"submodule_heads,omitempty"`
}

// ImportStage identifies the durable phase of a namespaced trunk ref import.
type ImportStage string

const (
	StageIntent          ImportStage = "intent"
	StageObjectsImported ImportStage = "objects_imported"
	StageMidRefPublish   ImportStage = "mid_ref_publish"
	StageRefsPublished   ImportStage = "refs_published"
)

// ImportJournalRecord persists the crash-recoverable state of a trunk ref import.
type ImportJournalRecord struct {
	JobID        string            `json:"job_id"`
	WorkerID     string            `json:"worker_id"`
	CheckpointID string            `json:"checkpoint_id"`
	TrunkDir     string            `json:"trunk_dir"`
	Stage        ImportStage       `json:"stage"`
	PlannedRefs  map[string]string `json:"planned_refs"`
	Published    map[string]string `json:"published"`
	ObjectsCount int64             `json:"objects_count"`
	UpdatedAt    time.Time         `json:"updated_at"`
}

// RefImportOptionFlags groups optional ref categories to export during trunk ref import.
type RefImportOptionFlags struct {
	IncludeDetachedHead bool
	IncludeTags         bool
	IncludeStashes      bool
	IncludeUnchanged    bool
}

// RefImportRequest specifies an idempotent namespaced ref import into the trunk.
type RefImportRequest struct {
	JobID        string
	WorkerID     string
	CheckpointID string
	TrunkDir     string
	Checkpoint   *CheckpointGitState
	Options      RefImportOptionFlags
	RefFilter    []string
	CrashHook    func(stage ImportStage) error
}

// RefImportResult is the structured result envelope for a completed trunk ref import.
type RefImportResult struct {
	JobID              string            `json:"job_id"`
	WorkerID           string            `json:"worker_id"`
	CheckpointID       string            `json:"checkpoint_id"`
	Stage              ImportStage       `json:"stage"`
	ImportedRefs       map[string]string `json:"imported_refs"`
	SourceToDest       map[string]string `json:"source_to_dest"`
	DeletedPrivateRefs []string          `json:"deleted_private_refs"`
	ObjectsImported    int64             `json:"objects_imported"`
}
