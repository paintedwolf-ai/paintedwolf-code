package project

import (
	"fmt"
)

// ValidateLifecycle checks the aggregate invariants shared by every registry.
func ValidateLifecycle(p *Project) error {
	if p == nil {
		return fmt.Errorf("nil project")
	}
	primaryCount := 0
	draftCount := 0
	for index, root := range p.Roots {
		if root.ProjectID != p.ID {
			return fmt.Errorf("root %s belongs to project %s", root.ID, root.ProjectID)
		}
		if root.IsPrimary {
			primaryCount++
		}
		if root.Kind == RootKindDraft {
			draftCount++
		} else if root.Kind != RootKindAttached {
			return fmt.Errorf("root %s has invalid kind %q", root.ID, root.Kind)
		}
		normalized, err := NormalizeRootDisplayLabel(root.Label)
		if err != nil || normalized != root.Label {
			return fmt.Errorf("root %s has invalid label %q", root.ID, root.Label)
		}
		if rootLabelTaken(p.Roots[:index], normalized, "") {
			return fmt.Errorf("%w: %s", ErrDuplicateRootLabel, root.Label)
		}
	}
	if len(p.Roots) == 0 {
		if primaryCount != 0 || draftCount != 0 {
			return fmt.Errorf("rootless project has root flags")
		}
	} else if primaryCount != 1 {
		return fmt.Errorf("project has %d primary roots", primaryCount)
	}
	if p.IsDraft {
		if len(p.Roots) != 1 || draftCount != 1 || !p.Roots[0].IsPrimary {
			return ErrDraftRootInvariant
		}
	} else if draftCount != 0 {
		return fmt.Errorf("saved project has draft root")
	}
	return nil
}

func rootByID(roots []Root, rootID string) (Root, bool) {
	for _, root := range roots {
		if root.ID == rootID {
			return root, true
		}
	}
	return Root{}, false
}
