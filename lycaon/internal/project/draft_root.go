package project

import (
	"context"
	"fmt"
	"os"
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
