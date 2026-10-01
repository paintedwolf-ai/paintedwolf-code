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
	"github.com/lycaon/lycaon/internal/projectroot"
)

const jobMetaFileName = "state.json"

// JobMeta is durable state for one worker branch.
type JobMeta struct {
	FormatVersion    int           `json:"format_version"`
	Roots            []JobMetaRoot `json:"roots"`
	SnapshotComplete bool          `json:"snapshot_complete"`
}

// JobMetaRoot identifies one project root in the branch tree.
type JobMetaRoot struct {
	ID        string `json:"id"`
	Path      string `json:"path"`
	Label     string `json:"label"`
	IsPrimary bool   `json:"is_primary"`
}

func jobMetaPath(metaDir string) string {
	return filepath.Join(metaDir, jobMetaFileName)
}

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
	if err := os.MkdirAll(metaDir, 0o700); err != nil {
		return err
	}
	meta.FormatVersion = 1
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

// LoadJobMeta reads strict branch metadata.
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
	if meta.FormatVersion != 1 {
		return JobMeta{}, fmt.Errorf("unsupported worker metadata format %d", meta.FormatVersion)
	}
	if _, err := layoutFromJobMeta(meta); err != nil {
		return JobMeta{}, err
	}
	return meta, nil
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
	meta := JobMeta{
		Roots: make([]JobMetaRoot, 0, len(layout.Roots)),
	}
	for _, root := range layout.Roots {
		meta.Roots = append(meta.Roots, JobMetaRoot{
			ID:        root.ID,
			Path:      filepath.Clean(root.Path),
			Label:     root.Label,
			IsPrimary: root.IsPrimary,
		})
	}
	return meta
}

func layoutFromJobMeta(meta JobMeta) (SandboxLayout, error) {
	roots := make([]projectroot.RootRef, 0, len(meta.Roots))
	for _, root := range meta.Roots {
		roots = append(roots, projectroot.RootRef{
			ID:        root.ID,
			Path:      root.Path,
			Label:     root.Label,
			IsPrimary: root.IsPrimary,
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
	branchRoot, err := cleanBranchRoot(branchRoot)
	if err != nil {
		return nil, err
	}
	meta, err := LoadJobMeta(enginepaths.MetaDirForBranchRoot(branchRoot))
	if err != nil {
		return nil, err
	}
	layout, err := layoutFromJobMeta(meta)
	if err != nil {
		return nil, err
	}
	return layout.Roots, nil
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
	if err := validateBranchDirectories(branchRoot, layout); err != nil {
		return SandboxLayout{}, err
	}
	return layout, nil
}

func validateBranchDirectories(branchRoot string, layout SandboxLayout) error {
	if len(layout.Roots) == 1 {
		return nil
	}
	for _, root := range layout.Roots {
		dir, err := projectroot.BranchDirForID(root.ID)
		if err != nil {
			return err
		}
		path := filepath.Join(branchRoot, dir)
		info, err := os.Lstat(path)
		if err != nil {
			return fmt.Errorf("worker branch root %q is unavailable: %w", root.ID, err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("worker branch root %q is not a directory", root.ID)
		}
	}
	return nil
}
