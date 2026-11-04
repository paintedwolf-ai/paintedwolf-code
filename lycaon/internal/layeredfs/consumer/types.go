// Package consumer implements indexed source inventory overlay, O(changed)
// workspace divergence, orientation inventory, branch survey, mount-backed
// evidence reads, checkpoint inspection, and bounded search across the worker
// lifecycle (#1664).
package consumer

import (
	"context"
	"errors"
	"io/fs"
	"time"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/observation"
)

var (
	// ErrDirectBackingAccessForbidden is returned when a live consumer attempts to read
	// backing storage or raw lower source directories instead of the worker mount.
	ErrDirectBackingAccessForbidden = errors.New("consumer: direct backing or unmounted lower storage access is forbidden for live consumers")
	ErrUnknownRoot                  = errors.New("consumer: unknown or unauthorized root id")
	ErrPathEscape                   = errors.New("consumer: path escapes root boundary")
	ErrCheckpointNotConfigured      = errors.New("consumer: checkpoint store is not configured")
	ErrMountNotConfigured           = errors.New("consumer: mounted workspace view is not configured")
)

// IndexCoverage describes the completeness of source index results for a query scope.
type IndexCoverage string

const (
	CoverageComplete IndexCoverage = "complete"
	CoveragePartial  IndexCoverage = "partial"
	CoverageStale    IndexCoverage = "stale"
	CoverageAbsent   IndexCoverage = "absent"
)

// IndexFreshness carries observation timestamp, coverage, and generation metadata for indexed entries.
// An index entry is never treated as an exact content observation, and an index miss is never proof of absence.
type IndexFreshness struct {
	ObservedAt       time.Time     `json:"observed_at"`
	Coverage         IndexCoverage `json:"coverage"`
	Stale            bool          `json:"stale,omitempty"`
	SourceGeneration uint64        `json:"source_generation,omitempty"`
	Truncated        bool          `json:"truncated,omitempty"`
}

// SourceIndexEntry represents one untouched path reported by the existing source index.
type SourceIndexEntry struct {
	Path       layeredfs.QualifiedPath `json:"path"`
	Kind       layeredfs.EntryKind     `json:"kind"`
	Mode       fs.FileMode             `json:"mode"`
	Size       int64                   `json:"size"`
	ModTime    time.Time               `json:"mod_time"`
	ObservedAt time.Time               `json:"observed_at"`
	Freshness  IndexFreshness          `json:"freshness"`
}

// SourceIndexQuery narrows an index lookup to specific roots, subroots, or path prefixes.
type SourceIndexQuery struct {
	RootIDs    []string          `json:"root_ids,omitempty"`
	Subroots   map[string]string `json:"subroots,omitempty"`
	PathPrefix string            `json:"path_prefix,omitempty"`
	MaxResults int               `json:"max_results,omitempty"`
}

// SourceIndexResult holds indexed entries and scope-wide coverage/freshness metadata.
type SourceIndexResult struct {
	Entries          []SourceIndexEntry `json:"entries"`
	Coverage         IndexCoverage      `json:"coverage"`
	ObservedAt       time.Time          `json:"observed_at"`
	SourceGeneration uint64             `json:"source_generation,omitempty"`
	Truncated        bool               `json:"truncated,omitempty"`
}

// SourceIndexProvider queries the existing host source index without creating a second global cache.
type SourceIndexProvider interface {
	QueryIndex(ctx context.Context, q SourceIndexQuery) (SourceIndexResult, error)
}

// SourceIndexFunc adapts a function to SourceIndexProvider.
type SourceIndexFunc func(ctx context.Context, q SourceIndexQuery) (SourceIndexResult, error)

// QueryIndex calls fn(ctx, q).
func (fn SourceIndexFunc) QueryIndex(ctx context.Context, q SourceIndexQuery) (SourceIndexResult, error) {
	if fn == nil {
		return SourceIndexResult{Coverage: CoverageAbsent}, nil
	}
	return fn(ctx, q)
}

// AccessSource identifies whether a consumer result came from the live mounted view,
// an immutable checkpoint blob store, live trunk fallback, or the index+journal overlay.
type AccessSource string

const (
	AccessSourceMountedView     AccessSource = "mounted_view"
	AccessSourceCheckpointStore AccessSource = "checkpoint_store"
	AccessSourceLiveTrunk       AccessSource = "live_trunk"
	AccessSourceIndexOverlay    AccessSource = "index_overlay"
)

// InventoryEntry represents one root-qualified entry in an orientation, survey, or directory listing.
type InventoryEntry struct {
	Path                 layeredfs.QualifiedPath `json:"path"`
	RootLabel            string                  `json:"root_label,omitempty"`
	Kind                 layeredfs.EntryKind     `json:"kind"`
	Layer                layeredfs.LayerSource   `json:"layer"`
	Mode                 fs.FileMode             `json:"mode"`
	Size                 int64                   `json:"size"`
	ModTime              time.Time               `json:"mod_time"`
	SHA256               string                  `json:"sha256,omitempty"`
	LinkTarget           string                  `json:"link_target,omitempty"`
	IsIndexDerived       bool                    `json:"is_index_derived"`
	ExactContentObserved bool                    `json:"exact_content_observed"`
	ObservedAt           time.Time               `json:"observed_at,omitempty"`
	Coverage             IndexCoverage           `json:"coverage,omitempty"`
	Freshness            IndexFreshness          `json:"freshness,omitempty"`
	Source               AccessSource            `json:"source"`
}

// DivergenceKind classifies a private worker change relative to the underlying source.
type DivergenceKind string

const (
	DivergenceAdded       DivergenceKind = "added"
	DivergenceModified    DivergenceKind = "modified"
	DivergenceDeleted     DivergenceKind = "deleted"
	DivergenceRenamed     DivergenceKind = "renamed"
	DivergenceReplacedDir DivergenceKind = "replaced_dir"
)

// DivergenceEntry describes one logical change in a worker view or published checkpoint.
type DivergenceEntry struct {
	Path              layeredfs.QualifiedPath  `json:"path"`
	PreviousPath      *layeredfs.QualifiedPath `json:"previous_path,omitempty"`
	Kind              DivergenceKind           `json:"kind"`
	EntryKind         layeredfs.EntryKind      `json:"entry_kind"`
	DirectoryReplaced bool                     `json:"directory_replaced,omitempty"`
	ContentChanged    bool                     `json:"content_changed,omitempty"`
	MetadataChanged   bool                     `json:"metadata_changed,omitempty"`
	FinalSHA256       string                   `json:"final_sha256,omitempty"`
	BaseSHA256        string                   `json:"base_sha256,omitempty"`
	Size              int64                    `json:"size,omitempty"`
}

// DivergenceSummary reports O(changed) workspace divergence and promotion preview entries.
type DivergenceSummary struct {
	ViewID        string                    `json:"view_id"`
	CheckpointID  string                    `json:"checkpoint_id,omitempty"`
	HasDivergence bool                      `json:"has_divergence"`
	Entries       []DivergenceEntry         `json:"entries"`
	Paths         []layeredfs.QualifiedPath `json:"paths"`
	Source        AccessSource              `json:"source"`
}

// OrientationInventory provides the model-request orientation snapshot for selected roots.
type OrientationInventory struct {
	ViewID          string                      `json:"view_id"`
	SelectedRoots   []string                    `json:"selected_roots"`
	Entries         []InventoryEntry            `json:"entries"`
	PrivateChanges  []DivergenceEntry           `json:"private_changes,omitempty"`
	Coverage        IndexCoverage               `json:"coverage"`
	ObservedAt      time.Time                   `json:"observed_at"`
	VisibilityFacts observation.VisibilityFacts `json:"visibility_facts"`
}

// SurveyOptions configures a branch or checkpoint inventory survey.
type SurveyOptions struct {
	RootIDs      []string          `json:"root_ids,omitempty"`
	Subroots     map[string]string `json:"subroots,omitempty"`
	PathPrefix   string            `json:"path_prefix,omitempty"`
	CheckpointID string            `json:"checkpoint_id,omitempty"`
	MaxEntries   int               `json:"max_entries,omitempty"`
}

// SurveyResult contains the merged inventory returned by SurveyBranch.
type SurveyResult struct {
	ViewID          string                      `json:"view_id"`
	CheckpointID    string                      `json:"checkpoint_id,omitempty"`
	Entries         []InventoryEntry            `json:"entries"`
	Coverage        IndexCoverage               `json:"coverage"`
	ObservedAt      time.Time                   `json:"observed_at"`
	Truncated       bool                        `json:"truncated,omitempty"`
	Source          AccessSource                `json:"source"`
	VisibilityFacts observation.VisibilityFacts `json:"visibility_facts"`
}

// EvidenceReadResult holds file bytes and provenance returned by ReadEvidence or ReadCheckpointEntry.
type EvidenceReadResult struct {
	Path               layeredfs.QualifiedPath             `json:"path"`
	Content            []byte                              `json:"content"`
	SHA256             string                              `json:"sha256"`
	Size               int64                               `json:"size"`
	Mode               fs.FileMode                         `json:"mode"`
	ModTime            time.Time                           `json:"mod_time"`
	Layer              layeredfs.LayerSource               `json:"layer"`
	Source             AccessSource                        `json:"source"`
	StoredInCheckpoint bool                                `json:"stored_in_checkpoint"`
	VisibilityMode     observation.UntouchedVisibilityMode `json:"visibility_mode"`
	Observation        observation.SourceObservation       `json:"observation"`
}

// SearchRequest configures a workspace content or path search.
type SearchRequest struct {
	Query                string   `json:"query"`
	RootIDs              []string `json:"root_ids,omitempty"`
	PathPrefix           string   `json:"path_prefix,omitempty"`
	CheckpointID         string   `json:"checkpoint_id,omitempty"`
	AllowScopedTraversal bool     `json:"allow_scoped_traversal,omitempty"`
	MaxFilesInspected    int      `json:"max_files_inspected,omitempty"`
	MaxDirsVisited       int      `json:"max_dirs_visited,omitempty"`
}

// SearchAccounting tracks bounded traversal and mount read operations during SearchWorkspace.
type SearchAccounting struct {
	IndexCandidates   int `json:"index_candidates"`
	PrivateCandidates int `json:"private_candidates"`
	DirsVisited       int `json:"dirs_visited"`
	FilesInspected    int `json:"files_inspected"`
	MountReads        int `json:"mount_reads"`
}

// SearchMatch represents one matched line in a workspace search.
type SearchMatch struct {
	Path              layeredfs.QualifiedPath        `json:"path"`
	Line              int                            `json:"line"`
	Snippet           string                         `json:"snippet"`
	Layer             layeredfs.LayerSource          `json:"layer"`
	Source            AccessSource                   `json:"source"`
	IsIndexDerived    bool                           `json:"is_index_derived"`
	ConfirmedViaMount bool                           `json:"confirmed_via_mount"`
	Observation       *observation.SourceObservation `json:"observation,omitempty"`
}

// SearchResult holds matches, coverage/incompleteness state, and resource accounting for SearchWorkspace.
type SearchResult struct {
	Matches         []SearchMatch               `json:"matches"`
	Coverage        IndexCoverage               `json:"coverage"`
	IncompleteScope bool                        `json:"incomplete_scope"`
	Canceled        bool                        `json:"canceled,omitempty"`
	Accounting      SearchAccounting            `json:"accounting"`
	VisibilityFacts observation.VisibilityFacts `json:"visibility_facts"`
}
