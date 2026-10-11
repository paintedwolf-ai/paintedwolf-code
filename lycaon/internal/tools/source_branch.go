package tools

import (
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sourcebranch"
)

// SourceBranch is the line of source history this call's writes record against.
func (tc ToolContext) SourceBranch(rootID string) (sourcebranch.ID, error) {
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
