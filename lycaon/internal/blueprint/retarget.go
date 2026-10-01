package blueprint

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/blueprintfile"
	"github.com/lycaon/lycaon/internal/fseffect"
)

// BeforeRetarget runs after the destination is chosen and before the file moves.
type BeforeRetarget func(ctx context.Context, projectID, from, to string)

// AfterRetarget runs after the file has moved so host bindings can follow.
type AfterRetarget func(ctx context.Context, projectID, from, to string) error

// RetargetToTitle moves a provisional convention file onto the declared title's slug.
// Locked (non-provisional) paths and placeholder titles are no-ops.
func (m *Manager) RetargetToTitle(ctx context.Context, projectID, path, title string) (string, error) {
	projectID = strings.TrimSpace(projectID)
	path = filepath.ToSlash(strings.TrimSpace(path))
	if projectID == "" {
		return "", fmt.Errorf("project_id required")
	}
	if err := ValidateConventionPath(path); err != nil {
		return "", err
	}
	if !IsProvisionalPath(path) {
		return path, nil
	}
	if blueprintfile.IsPlaceholderTitle(title) {
		return path, nil
	}
	normalized, err := NormalizeBlueprintDisplayTitle(title)
	if err != nil {
		return "", err
	}
	to, err := m.Store.FreePath(ctx, projectID, normalized, path)
	if err != nil {
		return "", err
	}
	if to == path {
		return path, nil
	}
	if m.BeforeRetarget != nil {
		m.BeforeRetarget(ctx, projectID, path, to)
	}
	if err := m.Store.Rename(ctx, projectID, path, to); err != nil {
		return "", err
	}
	m.relocateCriticEvidence(projectID, path, to)
	if m.Approvals != nil {
		if err := m.Approvals.Relocate(ctx, projectID, path, to); err != nil {
			return "", err
		}
	}
	if m.AfterRetarget != nil {
		if err := m.AfterRetarget(ctx, projectID, path, to); err != nil {
			return "", err
		}
	}
	return to, nil
}

func (m *Manager) relocateCriticEvidence(projectID, from, to string) {
	src, err := m.criticEvidencePath(projectID, from)
	if err != nil {
		return
	}
	dst, err := m.criticEvidencePath(projectID, to)
	if err != nil {
		return
	}
	root := filepath.Dir(src)
	if err := fseffect.Rename(root, filepath.Base(src), filepath.Base(dst)); err != nil && !os.IsNotExist(err) {
		m.dropCriticEvidence(projectID, from)
	}
}
