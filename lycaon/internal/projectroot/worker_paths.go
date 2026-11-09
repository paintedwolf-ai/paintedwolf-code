package projectroot

import "path/filepath"

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
