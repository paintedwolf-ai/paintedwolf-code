package project

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/enginepaths"
)

const draftScratchRootLabel = "Draft"

// DraftWorkspaceDir creates a project's scratch directory.
func DraftWorkspaceDir(projectID string) (string, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return "", fmt.Errorf("project id is required")
	}
	base, err := configdir.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir := enginepaths.DraftWorkspaceUnder(base, projectID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// ensureDraftScratchRoot attaches a draft's scratch root.
func ensureDraftScratchRoot(ctx context.Context, projectID string, attach func(AttachRootParams) (Root, error)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	scratch, err := DraftWorkspaceDir(projectID)
	if err != nil {
		return fmt.Errorf("draft workspace: %w", err)
	}
	return attachDraftScratchRoot(scratch, attach)
}

// ensureReservedDraftScratchRoot reuses the directory an interrupted reserved
// create left behind. Anything other than an empty, ordinary directory at the
// reserved path is preserved and refused.
func ensureReservedDraftScratchRoot(ctx context.Context, projectID string, attach func(AttachRootParams) (Root, error)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	base, err := configdir.UserConfigDir()
	if err != nil {
		return fmt.Errorf("draft workspace: %w", err)
	}
	scratch := enginepaths.DraftWorkspaceUnder(base, projectID)
	if filepath.Dir(scratch) != filepath.Clean(enginepaths.DraftsRootUnder(base)) {
		return ErrDraftWorkspaceOccupied
	}
	info, err := os.Lstat(scratch)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := os.MkdirAll(filepath.Dir(scratch), 0o700); err != nil {
			return fmt.Errorf("draft workspace: %w", err)
		}
		if err := os.Mkdir(scratch, 0o700); err != nil {
			return fmt.Errorf("draft workspace: %w", err)
		}
	case err != nil:
		return fmt.Errorf("draft workspace: %w", err)
	case !info.Mode().IsDir():
		return ErrDraftWorkspaceOccupied
	default:
		entries, readErr := os.ReadDir(scratch)
		if readErr != nil {
			return fmt.Errorf("draft workspace: %w", readErr)
		}
		if len(entries) > 0 {
			return ErrDraftWorkspaceOccupied
		}
	}
	return attachDraftScratchRoot(scratch, attach)
}

func attachDraftScratchRoot(scratch string, attach func(AttachRootParams) (Root, error)) error {
	isPrimary := true
	if _, err := attach(AttachRootParams{
		Path:      scratch,
		Label:     draftScratchRootLabel,
		IsPrimary: &isPrimary,
	}); err != nil {
		return err
	}
	return nil
}
