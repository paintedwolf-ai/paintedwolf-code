package checkpoint

import (
	"context"
	"fmt"
	"io/fs"
	"maps"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lycaon/lycaon/internal/layeredfs"
)

// TargetObservation captures the live integration target state for a path at Preview time.
type TargetObservation struct {
	Path            layeredfs.QualifiedPath `json:"path"`
	Kind            layeredfs.EntryKind     `json:"kind"`
	Mode            fs.FileMode             `json:"mode,omitempty"`
	Size            int64                   `json:"size,omitempty"`
	ModTime         time.Time               `json:"mod_time,omitempty"`
	SHA256          string                  `json:"sha256,omitempty"`
	LinkTarget      string                  `json:"link_target,omitempty"`
	DirectorySHA256 string                  `json:"directory_sha256,omitempty"`
	DirEmpty        bool                    `json:"dir_empty,omitempty"`
}

// PlannedActionKind identifies the promotion operation to execute on the target tree.
type PlannedActionKind string

const (
	ActionNoop                  PlannedActionKind = "noop"
	ActionWriteRegular          PlannedActionKind = "write_regular"
	ActionDelete                PlannedActionKind = "delete"
	ActionMkdir                 PlannedActionKind = "mkdir"
	ActionReplaceWithDirectory  PlannedActionKind = "replace_with_directory"
	ActionReplaceDirWithRegular PlannedActionKind = "replace_dir_with_regular"
	ActionSymlink               PlannedActionKind = "symlink"
	ActionHardLink              PlannedActionKind = "hard_link"
)

// PlannedChange describes one validated mutation in a PromotionPreview.
type PlannedChange struct {
	Path              layeredfs.QualifiedPath  `json:"path"`
	Action            PlannedActionKind        `json:"action"`
	Mode              fs.FileMode              `json:"mode,omitempty"`
	MergedBytes       []byte                   `json:"merged_bytes,omitempty"`
	MergedSHA256      string                   `json:"merged_sha256,omitempty"`
	LinkTarget        string                   `json:"link_target,omitempty"`
	HardLinkGroup     string                   `json:"hard_link_group,omitempty"`
	HardLinkSource    *layeredfs.QualifiedPath `json:"hard_link_source,omitempty"`
	Xattrs            map[string][]byte        `json:"xattrs,omitempty"`
	DirectoryReplaced bool                     `json:"directory_replaced,omitempty"`
}

// PromotionPreview binds planned changes, conflicts, and verification obligations
// to a specific immutable CheckpointID and observed target state.
type PromotionPreview struct {
	PreviewID            string                       `json:"preview_id"`
	CheckpointID         string                       `json:"checkpoint_id"`
	WorkerID             string                       `json:"worker_id"`
	TaskID               string                       `json:"task_id"`
	ProjectID            string                       `json:"project_id"`
	SourceGeneration     string                       `json:"source_generation"`
	TargetObservations   map[string]TargetObservation `json:"target_observations"`
	StaleSourceWarnings  []string                     `json:"stale_source_warnings,omitempty"`
	PlannedChanges       []PlannedChange              `json:"planned_changes"`
	Conflicts            []PromotionConflict          `json:"conflicts,omitempty"`
	RequiresVerification bool                         `json:"requires_verification"`
	CreatedAt            time.Time                    `json:"created_at"`
}

// PreviewRequest specifies ownership assertions and optional manual conflict resolutions.
type PreviewRequest struct {
	CheckpointID             string
	ExpectedWorkerID         string
	ExpectedTaskID           string
	ExpectedProjectID        string
	ExpectedSourceGeneration string
	Resolutions              map[layeredfs.QualifiedPath]ConflictResolution
}

// PromoterConfig configures a transactional Promoter.
type PromoterConfig struct {
	Store             *Store
	TargetRoots       map[string]string
	AuthorizedGitDirs []string
	RefImporter       func(ctx context.Context, txID, workerID string, git PrivateGitSnapshot) (map[string]string, error)
}

// Promoter executes exact-base three-way preview and transactional promotion.
type Promoter struct {
	cfg     PromoterConfig
	seq     atomic.Uint64
	applyMu sync.Mutex
	targets *promotionTargets
}

// NewPromoter creates a Promoter bound to a Store and target roots.
func NewPromoter(cfg PromoterConfig) (*Promoter, error) {
	if cfg.Store == nil || len(cfg.TargetRoots) == 0 {
		return nil, fmt.Errorf("checkpoint: Store and TargetRoots are required")
	}
	cfg.TargetRoots = maps.Clone(cfg.TargetRoots)
	return &Promoter{cfg: cfg, targets: &promotionTargets{store: cfg.Store, roots: cfg.TargetRoots}}, nil
}

// Preview computes a deterministic three-way promotion preview solely from the immutable checkpoint.
func (pr *Promoter) Preview(_ context.Context, req PreviewRequest) (*PromotionPreview, error) {
	m, err := pr.cfg.Store.LoadManifest(req.CheckpointID)
	if err != nil {
		return nil, err
	}
	if err := validatePreviewOwnership(m, req); err != nil {
		return nil, err
	}

	targetObs := make(map[string]TargetObservation, len(m.Entries))
	var warnings []string
	var planned []PlannedChange
	var conflicts []PromotionConflict
	requiresVerify := false

	for _, obs := range m.SourceObservations {
		cur, err := pr.targets.observeTarget(obs.Path)
		if err != nil {
			return nil, err
		}
		if cur.Kind != obs.Kind || (obs.SHA256 != "" && cur.SHA256 != obs.SHA256) {
			warnings = append(warnings, fmt.Sprintf("observed source path %s:%s changed since observation", obs.Path.RootID, obs.Path.Rel))
			requiresVerify = true
		}
	}

	groupPrimary := make(map[string]layeredfs.QualifiedPath)
	for _, entry := range m.Entries {
		cur, err := pr.targets.observeTarget(entry.Path)
		if err != nil {
			return nil, err
		}
		targetObs[pathKey(entry.Path)] = cur

		if cur.Kind == layeredfs.EntryDirectory && (entry.Deleted || entry.Kind == layeredfs.EntryAbsent || entry.DirectoryReplaced) {
			observed, err := pr.observeRemovedDirectory(m, entry.Path)
			if err != nil {
				return nil, err
			}
			if observed == nil {
				if resolution, ok := req.Resolutions[entry.Path]; ok && resolution.Choice == ResolutionKeepTarget {
					continue
				}
				conflict := newConflict(entry, cur, ConflictDirectoryReplaced, "directory contains target entries without matching deleted checkpoint bases")
				conflict.SupportedResolutions = []ResolutionChoice{ResolutionKeepTarget}
				conflicts = append(conflicts, *conflict)
				continue
			}
			maps.Copy(targetObs, observed)
		}

		change, conflict, mergedThreeWay, err := pr.evaluateEntry(entry, cur, groupPrimary)
		if err != nil {
			return nil, err
		}
		requiresVerify = requiresVerify || mergedThreeWay
		if res, ok := req.Resolutions[entry.Path]; ok {
			resolved, rErr := applyConflictResolution(entry, cur, res)
			if rErr != nil {
				return nil, rErr
			}
			if resolved.Action != ActionNoop {
				planned = append(planned, resolved)
			}
			requiresVerify = true
			continue
		}
		if conflict != nil {
			conflicts = append(conflicts, *conflict)
			continue
		}
		if change.Action != ActionNoop {
			planned = append(planned, change)
		}
	}

	return &PromotionPreview{
		PreviewID:            fmt.Sprintf("prev-%s-%d", m.CheckpointID, pr.seq.Add(1)),
		CheckpointID:         m.CheckpointID,
		WorkerID:             m.WorkerID,
		TaskID:               m.TaskID,
		ProjectID:            m.ProjectID,
		SourceGeneration:     m.SourceGeneration,
		TargetObservations:   targetObs,
		StaleSourceWarnings:  warnings,
		PlannedChanges:       planned,
		Conflicts:            conflicts,
		RequiresVerification: requiresVerify || len(conflicts) > 0,
		CreatedAt:            time.Now().UTC(),
	}, nil
}

func validatePreviewOwnership(m Manifest, req PreviewRequest) error {
	if req.ExpectedWorkerID != "" && m.WorkerID != req.ExpectedWorkerID {
		return fmt.Errorf("%w: worker got %q want %q", ErrOwnershipMismatch, m.WorkerID, req.ExpectedWorkerID)
	}
	if req.ExpectedTaskID != "" && m.TaskID != req.ExpectedTaskID {
		return fmt.Errorf("%w: task got %q want %q", ErrOwnershipMismatch, m.TaskID, req.ExpectedTaskID)
	}
	if req.ExpectedProjectID != "" && m.ProjectID != req.ExpectedProjectID {
		return fmt.Errorf("%w: project got %q want %q", ErrOwnershipMismatch, m.ProjectID, req.ExpectedProjectID)
	}
	if req.ExpectedSourceGeneration != "" && m.SourceGeneration != req.ExpectedSourceGeneration {
		return fmt.Errorf("%w: generation got %q want %q", ErrGenerationMismatch, m.SourceGeneration, req.ExpectedSourceGeneration)
	}
	return nil
}

func applyConflictResolution(entry ManifestEntry, cur TargetObservation, res ConflictResolution) (PlannedChange, error) {
	switch res.Choice {
	case ResolutionKeepTarget:
		return PlannedChange{Path: entry.Path, Action: ActionNoop}, nil
	case ResolutionApplyWorker:
		if entry.Deleted || entry.Kind == layeredfs.EntryAbsent {
			return PlannedChange{Path: entry.Path, Action: ActionDelete}, nil
		}
		return PlannedChange{
			Path:         entry.Path,
			Action:       ActionWriteRegular,
			Mode:         entry.Final.Mode,
			MergedSHA256: entry.Final.SHA256,
			Xattrs:       cloneXattrs(entry.Final.Xattrs),
		}, nil
	case ResolutionManualBytes:
		return PlannedChange{
			Path:         entry.Path,
			Action:       ActionWriteRegular,
			Mode:         pickMode(entry.Final.Mode, cur.Mode),
			MergedBytes:  append([]byte(nil), res.ManualBytes...),
			MergedSHA256: sha256Hex(res.ManualBytes),
			Xattrs:       cloneXattrs(entry.Final.Xattrs),
		}, nil
	default:
		return PlannedChange{}, fmt.Errorf("checkpoint: unsupported resolution choice %q", res.Choice)
	}
}

func pickMode(a, b fs.FileMode) fs.FileMode {
	if a != 0 {
		return a
	}
	if b != 0 {
		return b
	}
	return 0o644
}

func pathKey(qp layeredfs.QualifiedPath) string {
	return qp.RootID + ":" + qp.Rel
}
