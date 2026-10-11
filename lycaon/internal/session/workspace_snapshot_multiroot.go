package session

import (
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/session/promotionstate"
)

// SnapshotWorkspaceFromRoots fingerprints every project root.
// Baseline keys are root-qualified display paths (primary unprefixed, others @label/…).
func SnapshotWorkspaceFromRoots(roots []projectroot.RootRef) (map[string]WorkspaceFileFingerprint, error) {
	if len(roots) == 0 {
		return nil, nil
	}
	primary, err := projectroot.PrimaryRoot(roots)
	if err != nil {
		return nil, err
	}
	out := make(map[string]WorkspaceFileFingerprint)
	for _, root := range roots {
		snap, err := SnapshotWorkspace(root.Path)
		if err != nil {
			return nil, err
		}
		for rel, fp := range snap {
			abs := filepath.Join(root.Path, filepath.FromSlash(rel))
			key := projectroot.Qualify(primary, root, abs)
			out[key] = fp
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// DiffBranchWorkspaceSnapshot returns root-qualified changed paths under a worker branch.
func DiffBranchWorkspaceSnapshot(roots []projectroot.RootRef, branchRoot string, baseline map[string]WorkspaceFileFingerprint) []string {
	if baseline == nil || strings.TrimSpace(branchRoot) == "" {
		return nil
	}
	current, err := snapshotBranchWorkspace(roots, branchRoot)
	if err != nil {
		return nil
	}
	return diffWorkspaceSnapshots(baseline, current)
}

func snapshotBranchWorkspace(roots []projectroot.RootRef, branchRoot string) (map[string]WorkspaceFileFingerprint, error) {
	if len(roots) <= 1 {
		return SnapshotWorkspace(branchRoot)
	}
	primary, err := projectroot.PrimaryRoot(roots)
	if err != nil {
		return nil, err
	}
	out := make(map[string]WorkspaceFileFingerprint)
	for _, root := range roots {
		dir, err := projectroot.BranchDirForID(root.ID)
		if err != nil {
			return nil, err
		}
		subRoot := filepath.Join(branchRoot, dir)
		snap, err := SnapshotWorkspace(subRoot)
		if err != nil {
			return nil, err
		}
		for rel, fp := range snap {
			abs := filepath.Join(root.Path, filepath.FromSlash(rel))
			key := projectroot.Qualify(primary, root, abs)
			out[key] = fp
		}
	}
	return out, nil
}

// FilterOverlayPromoteCandidatePathsForRoots drops engine-internal paths across roots.
func FilterOverlayPromoteCandidatePathsForRoots(roots []projectroot.RootRef, paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	primary, _ := projectroot.PrimaryRoot(roots)
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		_, rel := resolveQualifiedPromotePath(roots, primary, p)
		if rel == "" {
			continue
		}
		if promotionstate.OmitOverlayPromotePath(rel) {
			continue
		}
		out = append(out, p)
	}
	return out
}

func resolveQualifiedPromotePath(roots []projectroot.RootRef, primary projectroot.RootRef, qualified string) (projectroot.RootRef, string) {
	qualified = strings.TrimSpace(qualified)
	if qualified == "" {
		return projectroot.RootRef{}, ""
	}
	if strings.HasPrefix(qualified, "@") {
		rest := strings.TrimPrefix(qualified, "@")
		slash := strings.Index(rest, "/")
		label := rest
		rel := ""
		if slash >= 0 {
			label = rest[:slash]
			rel = strings.TrimPrefix(rest[slash:], "/")
		}
		for _, r := range roots {
			if strings.EqualFold(r.Label, label) {
				if rel == "" {
					rel = "."
				}
				return r, filepath.ToSlash(filepath.Clean(rel))
			}
		}
		return projectroot.RootRef{}, ""
	}
	if primary.Path != "" {
		return primary, filepath.ToSlash(filepath.Clean(qualified))
	}
	return projectroot.RootRef{}, filepath.ToSlash(filepath.Clean(qualified))
}
