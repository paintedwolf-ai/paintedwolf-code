package jobstate

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/workspacebaseline"
	"github.com/lycaon/lycaon/pkg/api"
	"path/filepath"
	"strings"
)

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
