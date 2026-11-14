package projectroot

import (
	"path/filepath"
	"strings"
)

// WorkerBranchRelative maps root-qualified paths into the isolated branch layout.
func WorkerBranchRelative(roots []RootRef, activeRootID, modelPath string) (branchRel, displayPath string, err error) {
	abs, root, err := ResolveAbs(roots, activeRootID, modelPath)
	if err != nil {
		return "", "", err
	}
	displayPath = Qualify(roots, activeRootID, root, abs)
	scopeRel := ScopeRel(root, abs)
	if len(roots) == 1 {
		return scopeRel, displayPath, nil
	}
	dir, err := BranchDirForID(root.ID)
	if err != nil {
		return "", "", err
	}
	return filepath.ToSlash(filepath.Join(dir, scopeRel)), displayPath, nil
}

// RootForBranchRel identifies the attached root whose isolated branch directory
// prefixes a worker-branch-relative path, replacing display-string round trips
// as the structural check that a branch location belongs to the layout.
func RootForBranchRel(roots []RootRef, branchRel string) (RootRef, bool) {
	dir, _, _ := strings.Cut(filepath.ToSlash(filepath.Clean(branchRel)), "/")
	if dir == "" || dir == "." || dir == ".." {
		return RootRef{}, false
	}
	for _, root := range roots {
		if branchDir, err := BranchDirForID(root.ID); err == nil && branchDir == dir {
			return root, true
		}
	}
	return RootRef{}, false
}
