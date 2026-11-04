package worker

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/workerplacement"
	"github.com/lycaon/lycaon/internal/workspacebaseline"
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
	rel, ok, err := workerplacement.LogicalFromPhysical(dataDir, root)
	if err != nil {
		return "", "", err
	}
	if !ok {
		return "", "", fmt.Errorf("worker branch is outside the active installation")
	}
	return id, rel, nil
}
