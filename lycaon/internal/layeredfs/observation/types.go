// Package observation implements live trunk visibility coordination, cache
// invalidation dispatch, structured source observation provenance tracking,
// watcher overflow uncertainty handling, trunk.changed notices, and
// verification receipt staleness evaluation (#1665).
package observation

import (
	"errors"
	"fmt"
	"time"

	"github.com/lycaon/lycaon/internal/layeredfs"
)

var (
	ErrInvalidVisibilityFacts = errors.New("observation: invalid adapter visibility facts")
	ErrNilCore                = errors.New("observation: layeredfs Core is not bound")
)

// UntouchedVisibilityMode declares whether untouched paths resolve against live trunk or a fixed seed.
type UntouchedVisibilityMode string

const (
	VisibilityLive  UntouchedVisibilityMode = "live"
	VisibilityFixed UntouchedVisibilityMode = "fixed"
)

// AttributionCompleteness declares whether server read observations capture all reads
// or only a partial subset due to kernel page caching.
type AttributionCompleteness string

// ObservationCompleteness is an alias for AttributionCompleteness for observation records.
type ObservationCompleteness = AttributionCompleteness

const (
	CompletenessComplete            AttributionCompleteness = "exact"
	CompletenessPartialKernelCached AttributionCompleteness = "kernel_cached_partial"
	CompletenessPartial             AttributionCompleteness = CompletenessPartialKernelCached
)

// VisibilityFacts declares adapter cache and revalidation capabilities as explicit typed facts.
// Consumers must never infer visibility or cache behavior from runtime.GOOS or OS name strings.
type VisibilityFacts struct {
	AdapterName                        string                  `json:"adapter_name"`
	UntouchedVisibility                UntouchedVisibilityMode `json:"untouched_visibility"`
	SupportsInvalidationUpcall         bool                    `json:"supports_invalidation_upcall"`
	SupportsNegativeLookupInvalidation bool                    `json:"supports_negative_lookup_invalidation"`
	SupportsDirCookieInvalidation      bool                    `json:"supports_dir_cookie_invalidation"`
	AttributionCompleteness            AttributionCompleteness `json:"attribution_completeness"`
}

// ValidateVisibilityFacts verifies that adapter visibility facts are explicitly declared.
func ValidateVisibilityFacts(facts VisibilityFacts) error {
	switch facts.UntouchedVisibility {
	case VisibilityLive, VisibilityFixed:
	default:
		return fmt.Errorf("%w: unsupported untouched_visibility %q", ErrInvalidVisibilityFacts, facts.UntouchedVisibility)
	}
	switch facts.AttributionCompleteness {
	case CompletenessComplete, CompletenessPartialKernelCached:
	default:
		return fmt.Errorf("%w: unsupported attribution_completeness %q", ErrInvalidVisibilityFacts, facts.AttributionCompleteness)
	}
	return nil
}

// LiveMountVisibilityFacts returns explicit visibility facts for a live mounted adapter.
func LiveMountVisibilityFacts(
	adapterName string,
	invalidationUpcall bool,
	negativeLookupInvalidation bool,
	dirCookieInvalidation bool,
	completeness AttributionCompleteness,
) VisibilityFacts {
	return VisibilityFacts{
		AdapterName:                        adapterName,
		UntouchedVisibility:                VisibilityLive,
		SupportsInvalidationUpcall:         invalidationUpcall,
		SupportsNegativeLookupInvalidation: negativeLookupInvalidation,
		SupportsDirCookieInvalidation:      dirCookieInvalidation,
		AttributionCompleteness:            completeness,
	}
}

// MaterializedSeedVisibilityFacts returns explicit visibility facts for materialized fallback views.
func MaterializedSeedVisibilityFacts(adapterName string) VisibilityFacts {
	if adapterName == "" {
		adapterName = "materialized-fallback"
	}
	return VisibilityFacts{
		AdapterName:                        adapterName,
		UntouchedVisibility:                VisibilityFixed,
		SupportsInvalidationUpcall:         false,
		SupportsNegativeLookupInvalidation: false,
		SupportsDirCookieInvalidation:      false,
		AttributionCompleteness:            CompletenessPartialKernelCached,
	}
}

// InvalidationKind identifies the target of an adapter cache invalidation event.
type InvalidationKind string

const (
	InvalidateEntry        InvalidationKind = "invalidate_entry"
	InvalidateAttr         InvalidationKind = "invalidate_attr"
	InvalidateNegativeName InvalidationKind = "invalidate_negative_name"
	InvalidateDirectory    InvalidationKind = "invalidate_directory"
	InvalidateAllUncertain InvalidationKind = "invalidate_all_uncertain"
)

// InvalidationEvent describes a targeted cache invalidation dispatched to a mounted adapter.
type InvalidationEvent struct {
	Kind             InvalidationKind        `json:"kind"`
	ViewID           string                  `json:"view_id"`
	Path             layeredfs.QualifiedPath `json:"path"`
	ParentDir        layeredfs.QualifiedPath `json:"parent_dir,omitempty"`
	ChildName        string                  `json:"child_name,omitempty"`
	SourceGeneration uint64                  `json:"source_generation"`
	Reason           string                  `json:"reason,omitempty"`
}

// InvalidationSink receives targeted cache invalidations from a Tracker.
type InvalidationSink interface {
	Invalidate(ev InvalidationEvent)
}

// InvalidationFunc adapts a function to an InvalidationSink.
type InvalidationFunc func(ev InvalidationEvent)

// Invalidate calls fn(ev).
func (fn InvalidationFunc) Invalidate(ev InvalidationEvent) {
	if fn != nil {
		fn(ev)
	}
}

// ObservationProvenance distinguishes how a source observation was established.
type ObservationProvenance string

const (
	ProvenanceExactNativeRead   ObservationProvenance = "exact_native_read"
	ProvenanceServerFSRequest   ObservationProvenance = "server_fs_request"
	ProvenanceIndexDerived      ObservationProvenance = "index_derived"
	ProvenanceUncertainOverflow ObservationProvenance = "uncertain_overflow"
)

// SourceObservation records a structured observation of a path in a worker view.
type SourceObservation struct {
	ObservationID    string                    `json:"observation_id"`
	Path             layeredfs.QualifiedPath   `json:"path"`
	Layer            layeredfs.LayerSource     `json:"layer"`
	Kind             layeredfs.ObservationKind `json:"kind"`
	Provenance       ObservationProvenance     `json:"provenance"`
	Completeness     AttributionCompleteness   `json:"completeness"`
	Uncertain        bool                      `json:"uncertain"`
	SHA256           string                    `json:"sha256,omitempty"`
	Size             int64                     `json:"size"`
	ModTimeNS        int64                     `json:"mod_time_ns"`
	Ino              uint64                    `json:"ino,omitempty"`
	SourceGeneration uint64                    `json:"source_generation"`
}

// HeldDescriptorObservation binds an open file handle to the exact object version it opened.
type HeldDescriptorObservation struct {
	HandleID         layeredfs.HandleID      `json:"handle_id"`
	ObservationID    string                  `json:"observation_id"`
	Path             layeredfs.QualifiedPath `json:"path"`
	Layer            layeredfs.LayerSource   `json:"layer"`
	NodeID           layeredfs.NodeID        `json:"node_id"`
	OpenedDigest     string                  `json:"opened_digest,omitempty"`
	OpenedSize       int64                   `json:"opened_size"`
	OpenedIno        uint64                  `json:"opened_ino,omitempty"`
	OpenedModTimeNS  int64                   `json:"opened_mod_time_ns"`
	SourceGeneration uint64                  `json:"source_generation"`
}

// TrunkChangeKind classifies an external mutation on a live trunk root.
type TrunkChangeKind string

const (
	TrunkWriteInPlace  TrunkChangeKind = "write_in_place"
	TrunkAtomicReplace TrunkChangeKind = "atomic_replace"
	TrunkCreate        TrunkChangeKind = "create"
	TrunkDelete        TrunkChangeKind = "delete"
	TrunkRename        TrunkChangeKind = "rename"
	TrunkChmodOrAttr   TrunkChangeKind = "attr_change"
)

// TrunkChangeEvent describes a change to a path in the live trunk.
type TrunkChangeEvent struct {
	Path             layeredfs.QualifiedPath  `json:"path"`
	PreviousPath     *layeredfs.QualifiedPath `json:"previous_path,omitempty"`
	Kind             TrunkChangeKind          `json:"kind"`
	NewSHA256        string                   `json:"new_sha256,omitempty"`
	NewSize          int64                    `json:"new_size"`
	NewModTimeNS     int64                    `json:"new_mod_time_ns"`
	NewIno           uint64                   `json:"new_ino,omitempty"`
	NewDev           uint64                   `json:"new_dev,omitempty"`
	SourceGeneration uint64                   `json:"source_generation,omitempty"`
	OccurredAt       time.Time                `json:"occurred_at"`
}

// TrunkChangedNotice is the structured model notification emitted at the next model request.
// It informs the worker of changed observed paths without granting authority or selecting merge bases.
type TrunkChangedNotice struct {
	EventType            string                    `json:"event_type"`
	ViewID               string                    `json:"view_id"`
	AffectedPaths        []layeredfs.QualifiedPath `json:"affected_paths"`
	ObservationRefs      []string                  `json:"observation_refs"`
	UncertainOverflow    bool                      `json:"uncertain_overflow"`
	OverflowReasons      []string                  `json:"overflow_reasons,omitempty"`
	SourceGeneration     uint64                    `json:"source_generation"`
	GrantsAuthority      bool                      `json:"grants_authority"`
	EstablishesMergeBase bool                      `json:"establishes_merge_base"`
}

const (
	TrunkChangedEventType = "trunk.changed"

	StaleReasonObservedDependencyChanged          = "observed_dependency_changed"
	StaleReasonIncompleteAttributionScopeAdvanced = "incomplete_attribution_scope_generation_advanced"
	StaleReasonWatcherOverflowUncertain           = "watcher_overflow_uncertain"
	StaleReasonCheckpointMismatch                 = "checkpoint_mismatch"
)

// VerificationBinding binds a verification receipt to checkpoint, observed sources,
// attribution completeness, and enclosing scope source generation.
type VerificationBinding struct {
	ReceiptID                   string                  `json:"receipt_id"`
	ViewID                      string                  `json:"view_id"`
	CheckpointID                string                  `json:"checkpoint_id,omitempty"`
	ObservedSources             []SourceObservation     `json:"observed_sources"`
	ReadAttributionCompleteness AttributionCompleteness `json:"read_attribution_completeness"`
	EnclosingScopeRoots         []string                `json:"enclosing_scope_roots"`
	ScopeSourceGeneration       uint64                  `json:"scope_source_generation"`
	RootSourceGenerations       map[string]uint64       `json:"root_source_generations,omitempty"`
	BoundAt                     time.Time               `json:"bound_at"`
}

// ReceiptFreshnessResult reports whether a bound verification receipt remains fresh or is stale.
type ReceiptFreshnessResult struct {
	ReceiptID           string                    `json:"receipt_id"`
	CheckpointID        string                    `json:"checkpoint_id,omitempty"`
	Stale               bool                      `json:"stale"`
	StaleReason         string                    `json:"stale_reason,omitempty"`
	InvalidatingPaths   []layeredfs.QualifiedPath `json:"invalidating_paths,omitempty"`
	ObservationRefs     []string                  `json:"observation_refs,omitempty"`
	UncertainOverflow   bool                      `json:"uncertain_overflow"`
	EvaluatedGeneration uint64                    `json:"evaluated_generation"`
}

// StaleObservationHint provides structured stale-read review hints for promotion preview (#1663).
// It never substitutes for exact-base conflict checks or target revalidation.
type StaleObservationHint struct {
	Path                      layeredfs.QualifiedPath `json:"path"`
	ObservationID             string                  `json:"observation_id"`
	ObservedSHA256            string                  `json:"observed_sha256,omitempty"`
	CurrentSHA256             string                  `json:"current_sha256,omitempty"`
	ObservedGeneration        uint64                  `json:"observed_generation"`
	CurrentGeneration         uint64                  `json:"current_generation"`
	Provenance                ObservationProvenance   `json:"provenance"`
	Uncertain                 bool                    `json:"uncertain"`
	Reason                    string                  `json:"reason"`
	SubstitutesExactBaseCheck bool                    `json:"substitutes_exact_base_check"`
}
