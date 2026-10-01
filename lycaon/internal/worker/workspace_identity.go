package worker

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/workspacebaseline"
	"github.com/lycaon/lycaon/pkg/api"
)

func workerWorkspaceIdentity(ctx context.Context, database db.DBTX, root, baseline string) (string, string, error) {
	dataDir, err := db.Directory(ctx, database)
	if err != nil {
		return "", "", err
	}
	id, err := workspacebaseline.ID(baseline)
	if err != nil {
		return "", "", err
	}
	resolved, err := workspacebaseline.Path(dataDir, id)
	if err != nil {
		return "", "", err
	}
	if filepath.Clean(baseline) != resolved {
		return "", "", fmt.Errorf("worker baseline is outside the active installation")
	}
	rel, err := filepath.Rel(dataDir, root)
	if err != nil || !filepath.IsAbs(root) || !filepath.IsLocal(rel) || !strings.HasPrefix(filepath.ToSlash(rel), enginepaths.WorkerBranchesDirName+"/") {
		return "", "", fmt.Errorf("worker branch is outside the active installation")
	}
	return id, filepath.ToSlash(rel), nil
}

func resolveWorkerWorkspace(ctx context.Context, database db.DBTX, row db.WorkerJobs, task *api.WorkerTask) error {
	if !row.WorkspaceBaselineID.Valid && !row.WorkspaceOverlayID.Valid && !row.WorkspaceRelpath.Valid {
		return nil
	}
	dataDir, err := db.Directory(ctx, database)
	if err != nil {
		return err
	}
	task.WorkspaceBaselinePath, err = workspacebaseline.Path(dataDir, db.StringFromNull(row.WorkspaceBaselineID))
	if err != nil {
		return err
	}
	task.WorkspaceOverlayPath, err = workspacebaseline.Path(dataDir, db.StringFromNull(row.WorkspaceOverlayID))
	if err != nil {
		return err
	}
	if rel := db.StringFromNull(row.WorkspaceRelpath); rel != "" {
		if !filepath.IsLocal(rel) || !strings.HasPrefix(rel, enginepaths.WorkerBranchesDirName+"/") || strings.Contains(rel, "\\") {
			return fmt.Errorf("invalid worker branch reference")
		}
		task.WorkspaceRoot = filepath.Join(dataDir, filepath.FromSlash(rel))
	}
	return nil
}
