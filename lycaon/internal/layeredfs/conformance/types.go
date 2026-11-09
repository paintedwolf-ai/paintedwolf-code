// Package conformance implements the layered workspace conformance suite,
// deterministic work/resource counter assertions, representative tool workloads,
// host/installation evidence recorder, and measured performance acceptance (#1667).
package conformance

import (
	"time"

	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
)

// DurabilityEvidenceKind distinguishes process-crash, simulated storage faults,
// and real reboot/power-loss evidence (#1667).
type DurabilityEvidenceKind string

const (
	EvidenceKindDeterministic         DurabilityEvidenceKind = "deterministic_contract"
	EvidenceKindProcessCrash          DurabilityEvidenceKind = "process_crash"
	EvidenceKindSimulatedStorageFault DurabilityEvidenceKind = "simulated_storage_fault"
	EvidenceKindRebootPowerLoss       DurabilityEvidenceKind = "reboot_power_loss"
)

// ConformanceDomain identifies one of the 9 deterministic conformance domains (#1667).
type ConformanceDomain string

const (
	DomainPreparation ConformanceDomain = "preparation"
	DomainFullTurn    ConformanceDomain = "full_turn"
	DomainFilesystem  ConformanceDomain = "filesystem"
	DomainCoherence   ConformanceDomain = "coherence"
	DomainDurability  ConformanceDomain = "durability"
	DomainPromotion   ConformanceDomain = "promotion"
	DomainGit         ConformanceDomain = "git"
	DomainConfinement ConformanceDomain = "confinement"
	DomainLifecycle   ConformanceDomain = "lifecycle"
)

// WorkResourceCounters tracks deterministic operation and byte counts (#1667).
// Automated tests assert bounds on these counters rather than elapsed wall-clock time.
//
// RetainedImports and RetainedReuses are cumulative retained-store ACTIVITY flows
// (objects imported / objects reused) captured as deltas between two snapshots.
// They are not a retained-stock count: pack and reuse paths can count differently
// from unique Git objects, and a preexisting store starts from a nonzero baseline.
// Retained-storage / unique-object acceptance is measured separately (RepoInitCost,
// PerformanceAcceptanceReport.RetainedStorageGrowthB).
type WorkResourceCounters struct {
	LowerDirReads         int64 `json:"lower_dir_reads"`
	CapturedBytes         int64 `json:"captured_bytes"`
	ChangedNodes          int64 `json:"changed_nodes"`
	PhysicalCopyUps       int64 `json:"physical_copy_ups"`
	RPCCount              int64 `json:"rpc_count"`
	HandleLookups         int64 `json:"handle_lookups"`
	CacheEntries          int64 `json:"cache_entries"`
	RetainedImports       int64 `json:"retained_imports"`
	RetainedReuses        int64 `json:"retained_reuses"`
	LowerGitConfigTouches int64 `json:"lower_git_config_touches"`
}

// HostFacts records platform, installation, and mount/cache environment evidence (#1667).
type HostFacts struct {
	SourceCommit            string            `json:"source_commit"`
	OS                      string            `json:"os"`
	Arch                    string            `json:"arch"`
	KernelVersion           string            `json:"kernel_version"`
	InstallationShape       string            `json:"installation_shape"`
	BackingFilesystem       string            `json:"backing_filesystem"`
	AdapterName             adapter.AdapterID `json:"adapter_name"`
	MountCacheSettings      string            `json:"mount_cache_settings"`
	WorkloadFixture         string            `json:"workload_fixture"`
	NativeComparisonSummary string            `json:"native_comparison_summary"`
}

// RepoInitCost records first-worker Git object import vs second-worker reuse (#1667).
type RepoInitCost struct {
	ObjectsImported      int64 `json:"objects_imported"`
	BytesImported        int64 `json:"bytes_imported"`
	ReusedBySecondWorker bool  `json:"reused_by_second_worker"`
	SecondWorkerCopied   int64 `json:"second_worker_copied_objects"`
}

// PreparationDiagnostics exposes preparation and capture diagnostics (#1667).
// UserFacingWarnings is populated ONLY for action-needed or unsuccessful outcomes.
type PreparationDiagnostics struct {
	AdapterName         adapter.AdapterID  `json:"adapter_name"`
	SelectedRoots       []string           `json:"selected_roots"`
	FallbackReason      adapter.ReasonCode `json:"fallback_reason,omitempty"`
	PhysicalCopyUpCount int64              `json:"physical_copy_up_count"`
	LogicalChangeCount  int64              `json:"logical_change_count"`
	CapturedBytes       int64              `json:"captured_bytes"`
	CheckpointID        string             `json:"checkpoint_id,omitempty"`
	CheckpointResult    string             `json:"checkpoint_result,omitempty"`
	RepoInit            RepoInitCost       `json:"repo_init"`
	UserFacingWarnings  []string           `json:"user_facing_warnings,omitempty"`
}

// MaterializedComparisonReport contrasts eagercopy fallback against mounted claim (#1667).
type MaterializedComparisonReport struct {
	AdapterName           adapter.AdapterID        `json:"adapter_name"`
	PreparationKind       adapter.PreparationKind  `json:"preparation_kind"`
	UntouchedVisibility   adapter.SourceVisibility `json:"untouched_visibility"`
	LowerDirReadsAtClaim  int64                    `json:"lower_dir_reads_at_claim"`
	SeededEntries         int64                    `json:"seeded_entries"`
	SeededBytes           int64                    `json:"seeded_bytes"`
	FallbackReason        adapter.ReasonCode       `json:"fallback_reason"`
	LiveTrunkEditVisible  bool                     `json:"live_trunk_edit_visible"`
	MountedRootOpsAt1K    int64                    `json:"mounted_root_ops_at_1k"`
	MountedRootOpsAt2M    int64                    `json:"mounted_root_ops_at_2m"`
	UserWarningEmitted    bool                     `json:"user_warning_emitted"`
}

// DomainResult records the deterministic outcome and counters for one conformance domain.
type DomainResult struct {
	Domain       ConformanceDomain      `json:"domain"`
	AdapterName  adapter.AdapterID      `json:"adapter_name"`
	Passed       bool                   `json:"passed"`
	EvidenceKind DurabilityEvidenceKind `json:"evidence_kind"`
	Counters     WorkResourceCounters   `json:"counters"`
	Summary      string                 `json:"summary"`
}

// WorkloadComparison records a side-by-side comparison between layered and native execution.
type WorkloadComparison struct {
	Name              string   `json:"name"`
	Command           string   `json:"command"`
	NativeExitCode    int      `json:"native_exit_code"`
	LayeredExitCode   int      `json:"layered_exit_code"`
	OutputsMatch      bool     `json:"outputs_match"`
	MetadataMatch     bool     `json:"metadata_match"`
	ChangeSetsMatch   bool     `json:"change_sets_match"`
	ChangedPaths      []string `json:"changed_paths,omitempty"`
	DiagnosticSummary string   `json:"diagnostic_summary"`
}

// WorkloadMeasurement records cold and warm manual acceptance timings against native (#1667).
type WorkloadMeasurement struct {
	Workload       string        `json:"workload"`
	TargetSpec     string        `json:"target_spec"`
	NativeCold     time.Duration `json:"native_cold"`
	NativeWarm     time.Duration `json:"native_warm"`
	MountedCold    time.Duration `json:"mounted_cold"`
	MountedWarm    time.Duration `json:"mounted_warm"`
	WarmRatioToNat float64       `json:"warm_ratio_to_native"`
	Succeeded      bool          `json:"succeeded"`
}

// PerformanceAcceptanceReport aggregates diagnostic timing and storage growth evidence (#1667).
type PerformanceAcceptanceReport struct {
	AdapterName              adapter.AdapterID     `json:"adapter_name"`
	Workloads                []WorkloadMeasurement `json:"workloads"`
	FirstGitInit             RepoInitCost          `json:"first_git_init"`
	FirstGitInitDuration     time.Duration         `json:"first_git_init_duration"`
	SecondWorkerGitDuration  time.Duration         `json:"second_worker_git_duration"`
	CheckpointPauseLatency   time.Duration         `json:"checkpoint_pause_latency"`
	CancelRecoveryLatency    time.Duration         `json:"cancel_recovery_latency"`
	RetainedStorageGrowthB   int64                 `json:"retained_storage_growth_bytes"`
}

// QualificationEvidence bundles the complete #1667 qualification record for an adapter run.
type QualificationEvidence struct {
	Facts                  HostFacts                    `json:"facts"`
	Diagnostics            PreparationDiagnostics       `json:"diagnostics"`
	Domains                []DomainResult               `json:"domains"`
	MaterializedComparison MaterializedComparisonReport `json:"materialized_comparison"`
	Workloads              []WorkloadComparison         `json:"workloads"`
	Measurements           PerformanceAcceptanceReport  `json:"measurements"`
}
