package workspace

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/projectroot"
)

const (
	jobMetaFileName = "state.json"
	JobMetaFormatV1 = 1
	JobMetaFormatV2 = 2
)

// JobMetaPreparation describes layered adapter and fallback facts for a worker view (#1662).
type JobMetaPreparation struct {
	AdapterID       adapter.AdapterID        `json:"adapter_id,omitempty"`
	PreparationKind adapter.PreparationKind  `json:"preparation_kind,omitempty"`
	VisibilityMode  adapter.SourceVisibility `json:"visibility_mode,omitempty"`
	FallbackReason  adapter.ReasonCode       `json:"fallback_reason,omitempty"`
	CopiedEntries   int64                    `json:"copied_entries,omitempty"`
	CopiedBytes     int64                    `json:"copied_bytes,omitempty"`
	PendingGitInit  bool                     `json:"pending_git_init,omitempty"`
	AdapterFacts    *adapter.AdapterFacts    `json:"adapter_facts,omitempty"`
}

// JobMetaLineage describes layered view identity, journal, selection, and checkpoint refs (#1662).
type JobMetaLineage struct {
	ViewID                    string   `json:"view_id,omitempty"`
	PreparedViewID            string   `json:"prepared_view_id,omitempty"`
	SourceGeneration          int64    `json:"source_generation,omitempty"`
	JournalPath               string   `json:"journal_path,omitempty"`
	ParentCheckpointIDs       []string `json:"parent_checkpoint_ids,omitempty"`
	LastPublishedCheckpointID string   `json:"last_published_checkpoint_id,omitempty"`
	CheckpointStoreDir        string   `json:"checkpoint_store_dir,omitempty"`
	SelectedRootIDs           []string `json:"selected_root_ids,omitempty"`
	FocusPathHints            []string `json:"focus_path_hints,omitempty"`
}

// JobMeta is durable state for one worker branch.
type JobMeta struct {
	FormatVersion    int           `json:"format_version"`
	Roots            []JobMetaRoot `json:"roots"`
	SnapshotComplete bool          `json:"snapshot_complete"`
	JobMetaPreparation
	JobMetaLineage
}

func hasLayeredV2Fields(m JobMeta) bool {
	return m.AdapterID != "" || m.PreparationKind != "" || m.VisibilityMode != "" ||
		m.FallbackReason != "" || m.CopiedEntries != 0 || m.CopiedBytes != 0 ||
		m.PendingGitInit || m.ViewID != "" || m.PreparedViewID != "" ||
		m.SourceGeneration != 0 || m.JournalPath != "" || len(m.ParentCheckpointIDs) > 0 ||
		m.LastPublishedCheckpointID != "" || m.CheckpointStoreDir != "" || len(m.SelectedRootIDs) > 0 || len(m.FocusPathHints) > 0
}

// JobMetaRoot identifies one project root in the branch tree.
type JobMetaRoot struct {
	ID        string `json:"id"`
	Path      string `json:"path"`
	Label     string `json:"label"`
	IsPrimary bool   `json:"is_primary"`
}

func jobMetaPath(metaDir string) string { return filepath.Join(metaDir, jobMetaFileName) }

func cleanBranchRoot(branchRoot string) (string, error) {
	branchRoot = strings.TrimSpace(branchRoot)
	if branchRoot == "" || !filepath.IsAbs(branchRoot) {
		return "", fmt.Errorf("absolute worker branch root required")
	}
	return filepath.Clean(branchRoot), nil
}

// WriteJobMeta creates or replaces job meta on disk.
func WriteJobMeta(metaDir string, meta JobMeta) error {
	metaDir = strings.TrimSpace(metaDir)
	if metaDir == "" {
		return fmt.Errorf("job meta dir required")
	}
	if _, err := layoutFromJobMeta(meta); err != nil {
		return err
	}
	if meta.FormatVersion == 0 {
		meta.FormatVersion = JobMetaFormatV1
		if hasLayeredV2Fields(meta) {
			meta.FormatVersion = JobMetaFormatV2
		}
	}
	if meta.FormatVersion != JobMetaFormatV1 && meta.FormatVersion != JobMetaFormatV2 {
		return fmt.Errorf("unsupported worker metadata format %d", meta.FormatVersion)
	}
	if meta.FormatVersion == JobMetaFormatV1 && (meta.PreparationKind == adapter.PreparationMounted || meta.VisibilityMode == adapter.VisibilityLive) {
		return fmt.Errorf("format v1 worker metadata cannot declare mounted or live view")
	}
	if err := os.MkdirAll(metaDir, 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	_, err = fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.PathLocation(jobMetaPath(metaDir)),
		Source:   bytes.NewReader(append(raw, '\n')),
		Mode:     0o600,
		DirMode:  0o700,
	})
	return err
}

// LoadJobMeta reads strict branch metadata (Format-v1 or Format-v2).
func LoadJobMeta(metaDir string) (JobMeta, error) {
	raw, err := os.ReadFile(jobMetaPath(metaDir))
	if err != nil {
		return JobMeta{}, err
	}
	var meta JobMeta
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&meta); err != nil {
		return JobMeta{}, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return JobMeta{}, fmt.Errorf("unexpected data after job metadata")
		}
		return JobMeta{}, err
	}
	if meta.FormatVersion != JobMetaFormatV1 && meta.FormatVersion != JobMetaFormatV2 {
		return JobMeta{}, fmt.Errorf("unsupported worker metadata format %d", meta.FormatVersion)
	}
	if _, err := layoutFromJobMeta(meta); err != nil {
		return JobMeta{}, err
	}
	if err := normalizeLoadedJobMeta(&meta); err != nil {
		return JobMeta{}, err
	}
	return meta, nil
}

func normalizeLoadedJobMeta(meta *JobMeta) error {
	if meta.FormatVersion == JobMetaFormatV1 {
		if meta.PreparationKind == adapter.PreparationMounted || meta.VisibilityMode == adapter.VisibilityLive {
			return fmt.Errorf("format v1 worker metadata cannot declare mounted or live view")
		}
		if meta.AdapterID == "" {
			meta.AdapterID = adapter.AdapterEagerCopy
		}
		if meta.PreparationKind == "" {
			meta.PreparationKind = adapter.PreparationMaterialized
		}
		if meta.VisibilityMode == "" {
			meta.VisibilityMode = adapter.VisibilityFixed
		}
	} else if meta.VisibilityMode == "" {
		if meta.PreparationKind == adapter.PreparationMounted {
			meta.VisibilityMode = adapter.VisibilityLive
		} else if meta.PreparationKind == adapter.PreparationMaterialized {
			meta.VisibilityMode = adapter.VisibilityFixed
		}
	}
	if len(meta.SelectedRootIDs) == 0 {
		for _, r := range meta.Roots {
			meta.SelectedRootIDs = append(meta.SelectedRootIDs, r.ID)
		}
	}
	return nil
}

// UpgradeJobMetaToV2 upgrades a Format-v1 branch metadata file to Format-v2 while preserving
// retained materialized eagercopy semantics (VisibilityFixed) without relabeling it as live (#1662).
func UpgradeJobMetaToV2(metaDir string) (JobMeta, bool, error) {
	meta, err := LoadJobMeta(metaDir)
	if err != nil || meta.FormatVersion == JobMetaFormatV2 {
		return meta, false, err
	}
	meta.FormatVersion = JobMetaFormatV2
	meta.AdapterID = adapter.AdapterEagerCopy
	meta.PreparationKind = adapter.PreparationMaterialized
	meta.VisibilityMode = adapter.VisibilityFixed
	if err := WriteJobMeta(metaDir, meta); err != nil {
		return JobMeta{}, false, err
	}
	return meta, true, nil
}

func markSnapshotComplete(metaDir string) error {
	meta, err := LoadJobMeta(metaDir)
	if err != nil {
		return err
	}
	meta.SnapshotComplete = true
	return WriteJobMeta(metaDir, meta)
}

func jobMetaFromLayout(layout SandboxLayout) JobMeta {
	meta := JobMeta{Roots: make([]JobMetaRoot, 0, len(layout.Roots))}
	for _, root := range layout.Roots {
		meta.Roots = append(meta.Roots, JobMetaRoot{
			ID: root.ID, Path: filepath.Clean(root.Path), Label: root.Label, IsPrimary: root.IsPrimary,
		})
	}
	return meta
}

func layoutFromJobMeta(meta JobMeta) (SandboxLayout, error) {
	roots := make([]projectroot.RootRef, 0, len(meta.Roots))
	for _, root := range meta.Roots {
		roots = append(roots, projectroot.RootRef{
			ID: root.ID, Path: root.Path, Label: root.Label, IsPrimary: root.IsPrimary,
		})
	}
	layout := SandboxLayout{Roots: roots}
	if err := validateLayout(layout); err != nil {
		return SandboxLayout{}, err
	}
	primaryCount := 0
	ids := make(map[string]struct{}, len(roots))
	dirs := make(map[string]struct{}, len(roots))
	labels := make([]string, 0, len(roots))
	paths := make(map[string]struct{}, len(roots))
	for _, root := range roots {
		if root.IsPrimary {
			primaryCount++
		}
		if _, exists := ids[root.ID]; exists {
			return SandboxLayout{}, fmt.Errorf("duplicate sandbox root id %q", root.ID)
		}
		ids[root.ID] = struct{}{}
		path := filepath.Clean(root.Path)
		if !filepath.IsAbs(path) {
			return SandboxLayout{}, fmt.Errorf("sandbox root path is not absolute: %q", root.Path)
		}
		if _, exists := paths[path]; exists {
			return SandboxLayout{}, fmt.Errorf("duplicate sandbox root path %q", path)
		}
		paths[path] = struct{}{}
		if len(layout.Roots) > 1 {
			for _, label := range labels {
				if strings.EqualFold(label, root.Label) {
					return SandboxLayout{}, fmt.Errorf("duplicate sandbox root label %q", root.Label)
				}
			}
			labels = append(labels, root.Label)
			dir, err := projectroot.BranchDirForID(root.ID)
			if err != nil {
				return SandboxLayout{}, err
			}
			if _, exists := dirs[dir]; exists {
				return SandboxLayout{}, fmt.Errorf("duplicate sandbox branch directory %q", dir)
			}
			dirs[dir] = struct{}{}
		}
	}
	if primaryCount != 1 {
		return SandboxLayout{}, fmt.Errorf("sandbox layout requires exactly one primary root")
	}
	return layout, nil
}

// LoadBranchRoots returns the root topology recorded for branchRoot whether or
// not its tree is on disk. Callers that need the tree use LoadBranchLayout.
func LoadBranchRoots(branchRoot string) ([]projectroot.RootRef, error) {
	if clean, err := cleanBranchRoot(branchRoot); err != nil {
		return nil, err
	} else if meta, err := LoadJobMeta(enginepaths.MetaDirForBranchRoot(clean)); err != nil {
		return nil, err
	} else if layout, err := layoutFromJobMeta(meta); err != nil {
		return nil, err
	} else {
		return layout.Roots, nil
	}
}

// LoadBranchLayout returns the immutable root topology captured for branchRoot.
func LoadBranchLayout(branchRoot string) (SandboxLayout, error) {
	branchRoot, err := cleanBranchRoot(branchRoot)
	if err != nil {
		return SandboxLayout{}, err
	}
	info, err := os.Lstat(branchRoot)
	if err != nil {
		return SandboxLayout{}, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return SandboxLayout{}, fmt.Errorf("worker branch root is not a directory")
	}
	meta, err := LoadJobMeta(enginepaths.MetaDirForBranchRoot(branchRoot))
	if err != nil {
		return SandboxLayout{}, err
	}
	if !meta.SnapshotComplete {
		return SandboxLayout{}, fmt.Errorf("worker branch snapshot incomplete")
	}
	layout, err := layoutFromJobMeta(meta)
	if err != nil {
		return SandboxLayout{}, err
	}
	return layout, validateBranchDirectories(branchRoot, layout)
}
