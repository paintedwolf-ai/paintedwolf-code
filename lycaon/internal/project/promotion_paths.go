package project

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var (
	ErrNotDraft                     = fmt.Errorf("project is not a draft")
	ErrDraftRootInvariant           = fmt.Errorf("draft requires one primary draft root")
	ErrPromotionDestinationNotEmpty = fmt.Errorf("promotion destination is not empty")
)

// DraftPromotionRoot returns the engine-managed root a promotion transitions.
func DraftPromotionRoot(p *Project) (Root, error) {
	if p == nil || !p.IsDraft {
		return Root{}, ErrNotDraft
	}
	if len(p.Roots) != 1 || p.Roots[0].Kind != RootKindDraft || !p.Roots[0].IsPrimary {
		return Root{}, ErrDraftRootInvariant
	}
	return p.Roots[0], nil
}

func resolvePromotionDestination(destPath string) (string, error) {
	folder, err := ResolveExistingDir(destPath)
	if err != nil {
		return "", err
	}
	empty, err := dirIsEmpty(folder)
	if err != nil {
		return "", err
	}
	if !empty {
		return "", ErrPromotionDestinationNotEmpty
	}
	if err := defaultOpenPolicy.ValidateOpenPath(folder); err != nil {
		return "", err
	}
	return folder, nil
}

func dirIsEmpty(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if entry.Name() == ".DS_Store" {
			continue
		}
		return false, nil
	}
	return true, nil
}

// SamePath reports whether two paths resolve to the same directory.
func SamePath(a, b string) bool {
	a = filepath.Clean(strings.TrimSpace(a))
	b = filepath.Clean(strings.TrimSpace(b))
	if a == b {
		return true
	}
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}
