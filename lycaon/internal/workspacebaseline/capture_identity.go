package workspacebaseline

import (
	"fmt"
	"path"
	"strings"

	"github.com/lycaon/lycaon/internal/projectroot"
)

func validateIdentity(rootID, scopeRel string) error {
	if rootID == "" || strings.TrimSpace(rootID) != rootID || strings.ContainsRune(rootID, '\x00') {
		return fmt.Errorf("invalid workspace baseline root identity %q", rootID)
	}
	if scopeRel == "" || scopeRel == "." || path.IsAbs(scopeRel) || path.Clean(scopeRel) != scopeRel || strings.HasPrefix(scopeRel, "../") || scopeRel == ".." || strings.ContainsRune(scopeRel, '\x00') {
		return fmt.Errorf("invalid workspace baseline root-relative path %q", scopeRel)
	}
	return nil
}

func validateRoots(roots []projectroot.RootRef) error {
	if _, err := projectroot.PrimaryRoot(roots); err != nil {
		return fmt.Errorf("workspace baseline topology: %w", err)
	}
	seen := make(map[string]bool, len(roots))
	for _, root := range roots {
		if _, err := projectroot.BranchDirForID(root.ID); err != nil {
			return fmt.Errorf("workspace baseline root identity: %w", err)
		}
		if seen[root.ID] {
			return fmt.Errorf("duplicate workspace baseline root identity %q", root.ID)
		}
		seen[root.ID] = true
	}
	return nil
}

func rootForIdentity(roots []projectroot.RootRef, rootID string) (projectroot.RootRef, error) {
	for _, root := range roots {
		if root.ID == rootID {
			return root, nil
		}
	}
	return projectroot.RootRef{}, fmt.Errorf("workspace baseline root %q is absent from branch topology", rootID)
}
