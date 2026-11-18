package workspacebaseline

import (
	"fmt"
	"path"
	"strings"

	"github.com/lycaon/lycaon/internal/projectroot"
)

func validateRootID(rootID string) error {
	if rootID == "" || strings.TrimSpace(rootID) != rootID || strings.ContainsRune(rootID, '\x00') {
		return fmt.Errorf("invalid workspace baseline root identity %q", rootID)
	}
	return nil
}

func validateIdentity(rootID, scopeRel string) error {
	if err := validateRootID(rootID); err != nil {
		return err
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
		if err := validateRootID(root.ID); err != nil {
			return err
		}
		if seen[root.ID] {
			return fmt.Errorf("duplicate workspace baseline root identity %q", root.ID)
		}
		seen[root.ID] = true
	}
	return nil
}

func requireRoot(roots []projectroot.RootRef, rootID string) error {
	for _, root := range roots {
		if root.ID == rootID {
			return nil
		}
	}
	return fmt.Errorf("workspace baseline root %q is absent from branch topology", rootID)
}
