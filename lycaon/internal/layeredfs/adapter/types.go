// Package adapter implements the platform-neutral workspace adapter contract,
// capability-based selector, explicit workspace selection, and bounded
// materialized fallback feeding *layeredfs.Core (#1658).
package adapter

import (
	"context"
	"fmt"
	"time"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/privategit"
)

// AdapterID uniquely identifies a workspace adapter implementation.
type AdapterID string

const (
	AdapterFUSELinux AdapterID = "fuselinux"
	AdapterNFSDarwin AdapterID = "nfsdarwin"
	AdapterEagerCopy AdapterID = "eagercopy"
)

// PreparationKind describes whether a workspace view is mounted or materialized.
type PreparationKind string

const (
	PreparationMounted      PreparationKind = "mounted"
	PreparationMaterialized PreparationKind = "materialized"
)

// SourceVisibility describes whether untouched files reflect live lower changes or a fixed seed.
type SourceVisibility string

const (
	VisibilityLive  SourceVisibility = "live"
	VisibilityFixed SourceVisibility = "fixed"
)

// ReasonCode is a machine-readable reason for probe failure, fallback, or operation errors.
type ReasonCode string

const (
	ReasonNone                     ReasonCode = ""
	ReasonUnsupportedKernel        ReasonCode = "unsupported_kernel"
	ReasonUnsupportedPlatform      ReasonCode = "unsupported_platform"
	ReasonMountDenied              ReasonCode = "mount_denied"
	ReasonFUSEDeviceUnavailable    ReasonCode = "fuse_device_unavailable"
	ReasonFUSEHelperUnavailable    ReasonCode = "fuse_helper_unavailable"
	ReasonFUSEMountDenied          ReasonCode = "fuse_mount_denied"
	ReasonConfinementUnavailable   ReasonCode = "confinement_unavailable"
	ReasonHelperMissing            ReasonCode = "helper_missing"
	ReasonMountHelperUnavailable   ReasonCode = "mount_helper_unavailable"
	ReasonBackingConfinementFailed ReasonCode = "backing_confinement_failed"
	ReasonTransientStartupFailure  ReasonCode = "transient_startup_failure"
	ReasonProbeReadWriteFailed     ReasonCode = "probe_read_write_failed"
	ReasonCleanupFailed            ReasonCode = "cleanup_failed"
	ReasonBarrierFailed            ReasonCode = "barrier_failed"
	ReasonWorkspaceDoesNotFit      ReasonCode = "workspace_does_not_fit"
	ReasonCopyPassedBudget         ReasonCode = "copy_passed_budget"
	ReasonUnauthorizedRoot         ReasonCode = "unauthorized_root"
	ReasonInvalidSelection         ReasonCode = "invalid_selection"
	ReasonActiveViewMutated        ReasonCode = "active_view_mutated"
	ReasonViewNotFound             ReasonCode = "view_not_found"
	ReasonContextCanceled          ReasonCode = "context_canceled"
)

// CacheRevalidation describes the adapter's attribute/directory cache coherence guarantees.
type CacheRevalidation struct {
	Mode                       string        `json:"mode"`
	ExplicitInvalidation       bool          `json:"explicit_invalidation"`
	InvalidationVerified       bool          `json:"invalidation_verified"`
	NegativeLookupInvalidation bool          `json:"negative_lookup_invalidation"`
	DirCookieInvalidation      bool          `json:"dir_cookie_invalidation"`
	AttributeTTL               time.Duration `json:"attribute_ttl"`
}

// MetadataCapabilities describes filesystem metadata and locking support exposed by an adapter.
type MetadataCapabilities struct {
	SupportsSymlinks   bool `json:"supports_symlinks"`
	SupportsHardLinks  bool `json:"supports_hard_links"`
	SupportsXattrs     bool `json:"supports_xattrs"`
	SupportsChmod      bool `json:"supports_chmod"`
	SupportsTimestamps bool `json:"supports_timestamps"`
	SupportsPOSIXLocks bool `json:"supports_posix_locks"`
	SupportsFlock      bool `json:"supports_flock"`
	// SupportsFIFOOpen reports that a FIFO in the view can be opened. mkfifo, listing and removal work
	// on every adapter; the macOS NFS client alone refuses open(2) on a FIFO, whatever the server says.
	SupportsFIFOOpen bool `json:"supports_fifo_open"`
	// ClientMayRemoveEarlySidecar reports that a "._x" written before x exists may be removed by the
	// kernel client when x arrives. The request is indistinguishable from a worker's own removal, so
	// it is carried out. It may apply, not always does; a "._x" written after x stays on every adapter.
	ClientMayRemoveEarlySidecar bool `json:"client_may_remove_early_sidecar"`
}

// AdapterFacts reports the typed capabilities and preparation properties of an adapter.
type AdapterFacts struct {
	AdapterID                 AdapterID            `json:"adapter_id"`
	PreparationKind           PreparationKind      `json:"preparation_kind"`
	UntouchedSourceVisibility SourceVisibility     `json:"untouched_source_visibility"`
	CacheRevalidation         CacheRevalidation    `json:"cache_revalidation"`
	AttributionCompleteness   string               `json:"attribution_completeness,omitempty"`
	MetadataCapabilities      MetadataCapabilities `json:"metadata_capabilities"`
	SupportsPrivateGit        bool                 `json:"supports_private_git"`
	SupportsCheckpointBarrier bool                 `json:"supports_checkpoint_barrier"`
	FallbackReason            ReasonCode           `json:"fallback_reason,omitempty"`
}

// ProbeConfig configures a real confinement probe of an adapter.
type ProbeConfig struct {
	RuntimeDir         string
	ConfinementContext string
}

// ProbeResult captures the structured outcome of an adapter capability probe.
type ProbeResult struct {
	AdapterID             AdapterID  `json:"adapter_id"`
	Supported             bool       `json:"supported"`
	ReasonCode            ReasonCode `json:"reason_code,omitempty"`
	Diagnostic            string     `json:"diagnostic,omitempty"`
	MountExercised        bool       `json:"mount_exercised"`
	ReadExercised         bool       `json:"read_exercised"`
	WriteExercised        bool       `json:"write_exercised"`
	BackingDenialVerified bool       `json:"backing_denial_verified"`
	CleanedUp             bool       `json:"cleaned_up"`
	Attempts              int        `json:"attempts"`
}

// SourceIndexEstimate carries optional pre-computed entry and byte estimates for a root or subroot.
type SourceIndexEstimate struct {
	EstimatedEntries int64 `json:"estimated_entries"`
	EstimatedBytes   int64 `json:"estimated_bytes"`
}

// RootGrant represents an existing authorized root attached to a worker dispatch.
type RootGrant struct {
	RootID     string               `json:"root_id"`
	Label      string               `json:"label"`
	SourcePath string               `json:"source_path"`
	Subroot    string               `json:"subroot,omitempty"`
	IsPrimary  bool                 `json:"is_primary"`
	Estimate   *SourceIndexEstimate `json:"estimate,omitempty"`
}

// WorkspaceSelection narrows the prepared workspace to specific granted RootIDs or subroots.
type WorkspaceSelection struct {
	RootIDs  []string          `json:"root_ids,omitempty"`
	Subroots map[string]string `json:"subroots,omitempty"`
}

// MaterializeProgress reports live progress while a view's two trees are built.
type MaterializeProgress struct {
	ViewID        string `json:"view_id"`
	RootID        string `json:"root_id"`
	Entries       int64  `json:"entries"`
	LogicalBytes  int64  `json:"logical_bytes"`
	CopiedBytes   int64  `json:"copied_bytes"`
	GitReposBuilt int    `json:"git_repos_built"`
}

// MaterializeStats counts what building a view's two trees did. Entries and files count the source
// once; CopiedBytes counts every byte written, so a byte copy of both trees counts its content twice.
type MaterializeStats struct {
	Entries          int64 `json:"entries"`
	ClonedFiles      int64 `json:"cloned_files"`
	ClonedBytes      int64 `json:"cloned_bytes"`
	CopiedFiles      int64 `json:"copied_files"`
	CopiedBytes      int64 `json:"copied_bytes"`
	ContentBytesRead int64 `json:"content_bytes_read"`
}

// SeedGitRepoSpec describes a source repository (.git dir or gitlink) discovered during seed traversal.
type SeedGitRepoSpec struct {
	ViewID            string
	RootID            string
	RepoRelPath       string
	GitEntryRel       string
	SourceGitPath     string
	PrivateGitDir     string
	IsGitlink         bool
	GitlinkContent    string
	AuthorizedGitDirs []string
	MaxEntries        int64
	MaxBytes          int64
	RetainedStore     *privategit.RetainedObjectStore
}

// SeedGitInitResult reports the private metadata/object retention built for a discovered repo.
type SeedGitInitResult struct {
	PrivateMetaDir string `json:"private_meta_dir"`
	HeadRef        string `json:"head_ref"`
	ObjectCount    int64  `json:"object_count"`
	ObjectsReused  int64  `json:"objects_reused,omitempty"`
	BytesImported  int64  `json:"bytes_imported,omitempty"`
	BytesReused    int64  `json:"bytes_reused,omitempty"`
	AddedEntries   int64  `json:"added_entries"`
	AddedBytes     int64  `json:"added_bytes"`
	LeaseID        string `json:"lease_id,omitempty"`
}

// SeedGitInitializer builds private Git metadata/object retention during materialized seeding.
type SeedGitInitializer func(ctx context.Context, spec SeedGitRepoSpec) (SeedGitInitResult, error)

// FileCloner clones src to dst, reporting false when this pair of paths cannot clone.
type FileCloner func(src, dst string) (bool, error)

// PrepareRequest configures preparation of a single worker workspace view.
type PrepareRequest struct {
	ViewID          string
	RuntimeDir      string
	BackingDir      string
	TargetRootDirs  map[string]string
	Grants          []RootGrant
	Selection       WorkspaceSelection
	TaskPathHints   []string
	ParentLayers    []layeredfs.ParentLayer
	GitInterceptor  layeredfs.GitInterceptor
	ObservationSink layeredfs.ObservationSink
	// GitObjectStore retains materialized seed Git objects in the host's store instead of a per-root one.
	GitObjectStore *privategit.RetainedObjectStore
	// PristineDir holds each root's exact base under its root ID, beside the working tree and out of
	// a worker's reach. StatManifestPath is the view's stat manifest. Both default under BackingDir.
	PristineDir      string
	StatManifestPath string
	// CopyBudget stops a materialization that passes it, cleaning up only this preparation.
	CopyBudget     *CopyBudget
	SelectionFacts SelectionFacts
	ProgressFn     func(MaterializeProgress)
	SeedGitInit    SeedGitInitializer
	// CloneFile replaces the host clone primitive for a fixture.
	CloneFile       FileCloner
	QuiesceHook     func(ctx context.Context, viewID string) error
	FlushClientHook func(ctx context.Context, viewID string) error
}

// PreparedRoot describes one usable root mapping inside a prepared worker view.
type PreparedRoot struct {
	RootID        string `json:"root_id"`
	Label         string `json:"label"`
	SourcePath    string `json:"source_path"`
	Subroot       string `json:"subroot,omitempty"`
	WorkspacePath string `json:"workspace_path"`
	PristinePath  string `json:"pristine_path,omitempty"`
	IsPrimary     bool   `json:"is_primary"`
}

// PreparedView holds the allocated worker view identity, root mappings, Core engine, and facts.
type PreparedView struct {
	ViewID          string         `json:"view_id"`
	AdapterID       AdapterID      `json:"adapter_id"`
	Facts           AdapterFacts   `json:"facts"`
	Roots           []PreparedRoot `json:"roots"`
	PrimaryRootID   string         `json:"primary_root_id"`
	PrimaryWorkPath string         `json:"primary_work_path"`
	BackingDir      string         `json:"backing_dir"`
	RuntimeViewDir  string         `json:"runtime_view_dir"`
	// SourcePaths are the live trees this view serves, which its processes never reach directly.
	SourcePaths []string `json:"source_paths,omitempty"`
	// ListenerPorts are the loopback ports the view's server accepts its kernel client on.
	ListenerPorts   []uint16                     `json:"listener_ports,omitempty"`
	TaskPathHints   []string                     `json:"task_path_hints,omitempty"`
	SeededGitRepos  map[string]SeedGitInitResult `json:"seeded_git_repos,omitempty"`
	ObservedEntries int64                        `json:"observed_entries"`
	ObservedBytes   int64                        `json:"observed_bytes"`
	Selection       SelectionFacts               `json:"selection"`
	Materialize     MaterializeStats             `json:"materialize"`
	Core            *layeredfs.Core              `json:"-"`
}

// CommandAccessSpec supplies mounted/materialized paths and confinement facts to the command launcher.
// A process in a view starts through internal/exec with its Dir or program at these paths, whose
// helper enters them after exec; nothing starts one with a raw working directory.
type CommandAccessSpec struct {
	ViewID           string            `json:"view_id"`
	AdapterID        AdapterID         `json:"adapter_id"`
	PrimaryRootID    string            `json:"primary_root_id"`
	WorkDir          string            `json:"work_dir"`
	RootPaths        map[string]string `json:"root_paths"`
	HiddenBackingDir string            `json:"hidden_backing_dir"`
	DenyDirectSource []string          `json:"deny_direct_source"`
	Facts            AdapterFacts      `json:"facts"`
	AccessCount      int64             `json:"access_count"`
}

// BarrierReceipt is the explicit token and completion receipt returned by CheckpointBarrier.
type BarrierReceipt struct {
	ViewID                  string                 `json:"view_id"`
	AdapterID               AdapterID              `json:"adapter_id"`
	Token                   layeredfs.BarrierToken `json:"token"`
	WritersQuiesced         bool                   `json:"writers_quiesced"`
	OperationsDrained       bool                   `json:"operations_drained"`
	DirtyDataFlushed        bool                   `json:"dirty_data_flushed"`
	WritableMappingsFlushed bool                   `json:"writable_mappings_flushed"`
}

// ReleaseReceipt reports actual resource settlement when releasing a view.
type ReleaseReceipt struct {
	ViewID       string     `json:"view_id"`
	AdapterID    AdapterID  `json:"adapter_id"`
	Settled      bool       `json:"settled"`
	ReasonCode   ReasonCode `json:"reason_code,omitempty"`
	Diagnostic   string     `json:"diagnostic,omitempty"`
	CleanedPaths []string   `json:"cleaned_paths,omitempty"`
}

// AdapterError is a structured error with a machine-readable ReasonCode and diagnostic message.
type AdapterError struct {
	AdapterID  AdapterID  `json:"adapter_id"`
	Op         string     `json:"op"`
	ReasonCode ReasonCode `json:"reason_code"`
	Diagnostic string     `json:"diagnostic"`
	Cause      error      `json:"-"`
}

func (e *AdapterError) Error() string {
	if e.Diagnostic != "" {
		return fmt.Sprintf("adapter %s %s (%s): %s", e.AdapterID, e.Op, e.ReasonCode, e.Diagnostic)
	}
	return fmt.Sprintf("adapter %s %s (%s)", e.AdapterID, e.Op, e.ReasonCode)
}

func (e *AdapterError) Unwrap() error { return e.Cause }

// MountSupervisor is the live custody a mounted adapter holds over its views. Git bound through it
// is revoked when the serving process dies or the view is released.
type MountSupervisor interface {
	IsViewActive(viewID string) bool
	BindPrivateGit(viewID, workerID string, mgr *privategit.Manager, authorizedGitDirs ...string) error
}

// Adapter defines the platform-neutral workspace adapter contract (#1658).
type Adapter interface {
	ID() AdapterID
	Facts() AdapterFacts
	Probe(ctx context.Context, probeCfg ProbeConfig) (ProbeResult, error)
	Prepare(ctx context.Context, req PrepareRequest) (*PreparedView, error)
	CommandAccess(viewID string) (CommandAccessSpec, error)
	CheckpointBarrier(ctx context.Context, viewID string) (BarrierReceipt, func() error, error)
	Release(ctx context.Context, viewID string) (ReleaseReceipt, error)
}
