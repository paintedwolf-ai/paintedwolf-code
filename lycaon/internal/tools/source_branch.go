package tools

import (
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sourcebranch"
)

// SourceBranch is the line of source history this call's writes record against.
func (tc ToolContext) SourceBranch(rootID string) (sourcebranch.ID, error) {
	if rootID != "" && rootID == tc.Source.OutsideRoot.ID {
		return sourcebranch.Trunk, nil
	}
	branch, err := sourcebranch.FromKindAndJob(tc.Source.SourceWorkspaceKind, tc.Identity.WorkerJobID)
	if err == nil && !branch.IsWorker() {
		if branch, ok := tc.Source.ProjectRootBranches[rootID]; ok {
			return branch, nil
		}
		return tc.Source.ProjectSourceBranch, nil
	}
	return branch, err
}

// SourceLocation maps a touched path to the project root and root-relative path
// source history records. Absolute paths in a worker branch map back to the root
// the branch mirrors; other absolute paths are outside that branch.
func (tc ToolContext) SourceLocation(path string) (projectroot.RootRef, string, bool) {
	if branch := strings.TrimSpace(tc.Source.WorkerBranchRoot); branch != "" && filepath.IsAbs(path) {
		rel, ok := branchRel(branch, path)
		if !ok {
			return projectroot.RootRef{}, "", false
		}
		return branchSourceLocation(tc.Source.Roots, rel)
	}
	abs, root, err := projectroot.ResolveAbs(tc.Source.Roots, tc.Source.ActiveRootID, path)
	if err != nil || root.ID == "" {
		return projectroot.RootRef{}, "", false
	}
	return root, projectroot.ScopeRel(root, abs), true
}

// RecordedLocation maps a touched path to where source history records it: an
// attached root, or the outside namespace for any other host path.
func (tc ToolContext) RecordedLocation(path string) (projectroot.RootRef, string, bool) {
	if root, rel, ok := tc.SourceLocation(path); ok {
		return root, rel, true
	}
	return tc.outsideLocation(path)
}

func (tc ToolContext) outsideLocation(path string) (projectroot.RootRef, string, bool) {
	if tc.Source.OutsideRoot.ID == "" || !filepath.IsAbs(path) {
		return projectroot.RootRef{}, "", false
	}
	abs := fspath.CanonicalPath(path)
	if abs == "" {
		abs = filepath.Clean(path)
	}
	for _, root := range tc.unrecordedRoots() {
		if root != "" && confine.PathAtOrUnder(abs, fspath.CanonicalPath(root)) {
			return projectroot.RootRef{}, "", false
		}
	}
	rel, ok := projectroot.OutsideRel(abs)
	if !ok {
		return projectroot.RootRef{}, "", false
	}
	return tc.Source.OutsideRoot, rel, true
}

// unrecordedRoots are the folders whose paths never enter the outside
// namespace: the project's own trees and the host's scratch and state.
func (tc ToolContext) unrecordedRoots() []string {
	roots := append(confine.UnrecordedRoots(), tc.Source.WorkerBranchRoot, tc.Host.SessionScratchDir, tc.Host.SessionOutputDir, tc.Host.HostDataDir)
	for _, root := range tc.Source.Roots {
		roots = append(roots, root.Path)
	}
	return roots
}

// branchRel accepts the branch root as configured or canonicalized, since
// the sandbox boundary returns symlink-resolved paths.
func branchRel(branch, abs string) (string, bool) {
	abs = filepath.Clean(abs)
	candidates := []string{filepath.Clean(branch)}
	if canonical, err := filepath.EvalSymlinks(branch); err == nil && canonical != candidates[0] {
		candidates = append(candidates, canonical)
	}
	for _, candidate := range candidates {
		rel, err := filepath.Rel(candidate, abs)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		return filepath.ToSlash(rel), true
	}
	return "", false
}

// branchSourceLocation inverts the branch layout: one root fills the branch,
// several roots each own the directory BranchDirForID names.
func branchSourceLocation(roots []projectroot.RootRef, rel string) (projectroot.RootRef, string, bool) {
	switch len(roots) {
	case 0:
		return projectroot.RootRef{}, "", false
	case 1:
		if roots[0].ID == "" {
			return projectroot.RootRef{}, "", false
		}
		return roots[0], rel, true
	}
	dir, rest, _ := strings.Cut(rel, "/")
	for _, root := range roots {
		if rootDir, err := projectroot.BranchDirForID(root.ID); err == nil && rootDir == dir {
			if rest == "" {
				rest = "."
			}
			return root, rest, true
		}
	}
	return projectroot.RootRef{}, "", false
}
