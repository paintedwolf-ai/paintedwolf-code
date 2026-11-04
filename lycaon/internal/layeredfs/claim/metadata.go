package claim

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
)

// RootMappingRecord persists one selected root mapping inside a worker view (#1662).
type RootMappingRecord struct {
	RootID        string `json:"root_id"`
	Label         string `json:"label"`
	SourcePath    string `json:"source_path"`
	Subroot       string `json:"subroot,omitempty"`
	WorkspacePath string `json:"workspace_path"`
	IsPrimary     bool   `json:"is_primary"`
}

// AdapterMetadataFacts persists the adapter identity, preparation kind, fallback reason, and seed cost.
type AdapterMetadataFacts struct {
	AdapterID            adapter.AdapterID        `json:"adapter_id"`
	PreparationKind      adapter.PreparationKind  `json:"preparation_kind"`
	SourceVisibility     adapter.SourceVisibility `json:"source_visibility"`
	FallbackReason       adapter.ReasonCode       `json:"fallback_reason,omitempty"`
	CopiedEntries        int64                    `json:"copied_entries"`
	CopiedBytes          int64                    `json:"copied_bytes"`
	PendingGitInit       bool                     `json:"pending_git_init"`
	SeededGitRepos       []string                 `json:"seeded_git_repos,omitempty"`
	IsLegacyMaterialized bool                     `json:"is_legacy_materialized,omitempty"`
}

// CheckpointMetadataRefs persists parent, latest published, and pending/failed checkpoint references.
type CheckpointMetadataRefs struct {
	ParentCheckpointIDs       []string           `json:"parent_checkpoint_ids,omitempty"`
	LastPublishedCheckpointID string             `json:"last_published_checkpoint_id,omitempty"`
	PendingCheckpointID       string             `json:"pending_checkpoint_id,omitempty"`
	FailedCheckpointReason    adapter.ReasonCode `json:"failed_checkpoint_reason,omitempty"`
}

// ViewMetadataV2 is the Format-v2 persisted branch/view metadata record (#1662).
// It records durable identity and reconstructible state, never claiming a live mount survived restart.
type ViewMetadataV2 struct {
	FormatVersion       int                     `json:"format_version"`
	Identity            ClaimIdentity           `json:"identity"`
	SelectedRoots       []RootMappingRecord     `json:"selected_roots"`
	FocusPathHints      []string                `json:"focus_path_hints,omitempty"`
	Adapter             AdapterMetadataFacts    `json:"adapter"`
	JournalPath         string                  `json:"journal_path"`
	Checkpoints         CheckpointMetadataRefs  `json:"checkpoints"`
	LegacyCapturedBases []layeredfs.BaseCapture `json:"legacy_captured_bases,omitempty"`
	CommittedAt         time.Time               `json:"committed_at"`
}

// LegacyBranchMetadataV1 represents Format-v1 retained materialized branch metadata (#1662).
type LegacyBranchMetadataV1 struct {
	FormatVersion    int                     `json:"format_version"`
	JobID            string                  `json:"job_id"`
	ProjectID        string                  `json:"project_id"`
	SourceGeneration uint64                  `json:"source_generation"`
	BranchRoot       string                  `json:"branch_root"`
	Roots            []RootMappingRecord     `json:"roots"`
	FocusPathHints   []string                `json:"focus_path_hints,omitempty"`
	CapturedBases    []layeredfs.BaseCapture `json:"captured_bases"`
	LastCheckpointID string                  `json:"last_checkpoint_id,omitempty"`
}

type versionProbe struct {
	FormatVersion *int `json:"format_version"`
}

// SaveViewMetadata validates and atomically writes Format-v2 metadata to path.
func SaveViewMetadata(path string, meta ViewMetadataV2) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("%w: metadata path is required", ErrInvalidMetadata)
	}
	meta.FormatVersion = FormatVersionV2
	if meta.CommittedAt.IsZero() {
		meta.CommittedAt = time.Now().UTC()
	}
	if err := validateViewMetadataV2(meta); err != nil {
		return err
	}
	return writeJSONAtomically(path, meta)
}

// LoadViewMetadata loads Format-v2 metadata or upgrades Format-v1 materialized metadata in memory.
// Unknown versions or invalid shapes are refused without mutating or deleting the file on disk.
func LoadViewMetadata(path string) (ViewMetadataV2, bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ViewMetadataV2{}, false, err
	}
	var vp versionProbe
	if err := json.Unmarshal(raw, &vp); err != nil || vp.FormatVersion == nil {
		return ViewMetadataV2{}, false, fmt.Errorf("%w: missing or unreadable format_version", ErrInvalidMetadata)
	}
	switch *vp.FormatVersion {
	case FormatVersionV2:
		var v2 ViewMetadataV2
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&v2); err != nil {
			return ViewMetadataV2{}, false, fmt.Errorf("%w: %v", ErrInvalidMetadata, err)
		}
		if err := validateViewMetadataV2(v2); err != nil {
			return ViewMetadataV2{}, false, err
		}
		return v2, false, nil
	case FormatVersionV1:
		var v1 LegacyBranchMetadataV1
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&v1); err != nil {
			return ViewMetadataV2{}, false, fmt.Errorf("%w: %v", ErrInvalidMetadata, err)
		}
		upgraded, err := upgradeV1ToV2(v1)
		if err != nil {
			return ViewMetadataV2{}, false, err
		}
		return upgraded, true, nil
	default:
		return ViewMetadataV2{}, false, fmt.Errorf("%w: version %d", ErrUnsupportedMetadataVersion, *vp.FormatVersion)
	}
}

// UpgradeMetadataFile upgrades a Format-v1 metadata file on disk to Format-v2 atomically
// while preserving its materialized captured bases, fixed visibility, and retained checkpoint result.
func UpgradeMetadataFile(path string) (ViewMetadataV2, error) {
	meta, upgraded, err := LoadViewMetadata(path)
	if err != nil {
		return ViewMetadataV2{}, err
	}
	if !upgraded {
		return meta, nil
	}
	if err := SaveViewMetadata(path, meta); err != nil {
		return ViewMetadataV2{}, err
	}
	return meta, nil
}

func upgradeV1ToV2(v1 LegacyBranchMetadataV1) (ViewMetadataV2, error) {
	if strings.TrimSpace(v1.JobID) == "" || len(v1.Roots) == 0 {
		return ViewMetadataV2{}, fmt.Errorf("%w: v1 metadata requires job_id and roots", ErrInvalidMetadata)
	}
	for _, r := range v1.Roots {
		if strings.TrimSpace(r.RootID) == "" || strings.TrimSpace(r.SourcePath) == "" || strings.TrimSpace(r.WorkspacePath) == "" {
			return ViewMetadataV2{}, fmt.Errorf("%w: v1 root mapping missing required paths", ErrInvalidMetadata)
		}
	}
	var copiedBytes int64
	for _, b := range v1.CapturedBases {
		copiedBytes += b.Size
	}
	journalPath := filepath.Join(filepath.Dir(v1.BranchRoot), "journal")
	if strings.TrimSpace(v1.BranchRoot) == "" {
		journalPath = filepath.Join(filepath.Dir(v1.Roots[0].WorkspacePath), "journal")
	}
	v2 := ViewMetadataV2{
		FormatVersion: FormatVersionV2,
		Identity: ClaimIdentity{
			WorkerID:         v1.JobID,
			ProjectID:        v1.ProjectID,
			ViewID:           "legacy-v1-" + v1.JobID,
			SourceGeneration: v1.SourceGeneration,
		},
		SelectedRoots:  append([]RootMappingRecord(nil), v1.Roots...),
		FocusPathHints: append([]string(nil), v1.FocusPathHints...),
		Adapter: AdapterMetadataFacts{
			AdapterID:            adapter.AdapterEagerCopy,
			PreparationKind:      adapter.PreparationMaterialized,
			SourceVisibility:     adapter.VisibilityFixed,
			CopiedEntries:        int64(len(v1.CapturedBases)),
			CopiedBytes:          copiedBytes,
			PendingGitInit:       false,
			IsLegacyMaterialized: true,
		},
		JournalPath: journalPath,
		Checkpoints: CheckpointMetadataRefs{
			LastPublishedCheckpointID: v1.LastCheckpointID,
		},
		LegacyCapturedBases: append([]layeredfs.BaseCapture(nil), v1.CapturedBases...),
		CommittedAt:         time.Now().UTC(),
	}
	if err := validateViewMetadataV2(v2); err != nil {
		return ViewMetadataV2{}, err
	}
	return v2, nil
}

func validateViewMetadataV2(m ViewMetadataV2) error {
	if m.FormatVersion != FormatVersionV2 {
		return fmt.Errorf("%w: expected version 2, got %d", ErrUnsupportedMetadataVersion, m.FormatVersion)
	}
	if strings.TrimSpace(m.Identity.WorkerID) == "" || strings.TrimSpace(m.Identity.ViewID) == "" {
		return fmt.Errorf("%w: worker_id and view_id are required", ErrInvalidMetadata)
	}
	if len(m.SelectedRoots) == 0 {
		return fmt.Errorf("%w: at least one selected root mapping is required", ErrInvalidMetadata)
	}
	for _, r := range m.SelectedRoots {
		if strings.TrimSpace(r.RootID) == "" || strings.TrimSpace(r.SourcePath) == "" || strings.TrimSpace(r.WorkspacePath) == "" {
			return fmt.Errorf("%w: selected root %q has empty source or workspace path", ErrInvalidMetadata, r.RootID)
		}
	}
	if strings.TrimSpace(string(m.Adapter.AdapterID)) == "" || strings.TrimSpace(m.JournalPath) == "" {
		return fmt.Errorf("%w: adapter_id and journal_path are required", ErrInvalidMetadata)
	}
	if m.Adapter.IsLegacyMaterialized {
		if m.Adapter.AdapterID != adapter.AdapterEagerCopy || m.Adapter.PreparationKind != adapter.PreparationMaterialized || m.Adapter.SourceVisibility != adapter.VisibilityFixed {
			return fmt.Errorf("%w: legacy materialized branch cannot be relabeled as a live mounted view", ErrInvalidMetadata)
		}
	}
	return nil
}

func writeJSONAtomically(path string, v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	_, err = fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.PathLocation(path),
		Source:   bytes.NewReader(raw),
		Mode:     0o600,
		DirMode:  0o700,
	})
	return err
}
