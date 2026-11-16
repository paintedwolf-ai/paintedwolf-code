package worker

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/workerplacement"
	"github.com/lycaon/lycaon/internal/workspace"
	"github.com/lycaon/lycaon/internal/workspacebaseline"
)

// SetChildSessionID records the child session for transcript drill-down.
func (s *SQLStore) SetChildSessionID(ctx context.Context, jobID, childSessionID string) error {
	jobID = strings.TrimSpace(jobID)
	childSessionID = strings.TrimSpace(childSessionID)
	if jobID == "" || childSessionID == "" {
		return fmt.Errorf("job_id and child_session_id required")
	}
	return s.mutateInTx(ctx, jobID, func(q *db.Queries) error {
		if err := q.SetWorkerJobChildSession(ctx, db.SetWorkerJobChildSessionParams{
			ChildSessionID: db.NullString(childSessionID),
			ID:             jobID,
		}); err != nil {
			return err
		}
		_, err := q.BindWorkerChildByJob(ctx, db.BindWorkerChildByJobParams{
			ChildSessionID: db.NullString(childSessionID), UpdatedAt: db.FormatTime(time.Now().UTC()), JobID: jobID,
		})
		return err
	})
}

// SetWorkerWorkspace binds one branch snapshot and its exact baseline.
func (s *SQLStore) SetWorkerWorkspace(ctx context.Context, jobID, root, baselinePath string) (bool, error) {
	jobID = strings.TrimSpace(jobID)
	root = strings.TrimSpace(root)
	baselinePath = strings.TrimSpace(baselinePath)
	if jobID == "" || root == "" || (baselinePath == "" && !workspace.BranchUsesCheckpoints(root)) {
		return false, nil
	}
	baselineID, relpath, err := s.workspaceIdentity(root, baselinePath)
	if err != nil {
		return false, err
	}
	return s.casInTx(ctx, jobID, func(q *db.Queries) (int64, error) {
		return q.SetWorkerJobWorkspace(ctx, db.SetWorkerJobWorkspaceParams{
			WorkspaceRelpath:    db.NullString(relpath),
			WorkspaceBaselineID: db.NullString(baselineID),
			ID:                  jobID,
		})
	})
}

// PendingOverlay is a completed job whose overlay awaits promotion.
type PendingOverlay struct {
	JobID string
	// ParentSessionID is the session that dispatched the job.
	ParentSessionID string
}

// ListPendingOverlays returns completed jobs whose overlays await promotion, oldest first.
func (s *SQLStore) ListPendingOverlays(ctx context.Context) ([]PendingOverlay, error) {
	rows, err := s.queries.ListPendingOverlayWorkerJobs(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]PendingOverlay, 0, len(rows))
	for _, row := range rows {
		out = append(out, PendingOverlay{JobID: row.ID, ParentSessionID: strings.TrimSpace(row.ParentSessionID.String)})
	}
	return out, nil
}

// ClearWorkerWorkspace clears branch metadata after merge or abort.
func (s *SQLStore) ClearWorkerWorkspace(ctx context.Context, jobID string) error {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return nil
	}
	return s.mutateInTx(ctx, jobID, func(q *db.Queries) error {
		return q.ClearWorkerJobWorkspaceRoot(ctx, jobID)
	})
}

// workspaceIdentity maps a branch and its baseline to their stored addresses.
func (s *SQLStore) workspaceIdentity(root, baseline string) (string, string, error) {
	dataDir, err := s.jobs.DataDir()
	if err != nil {
		return "", "", err
	}
	// A branch captured by checkpoint records no baseline.
	id := ""
	if strings.TrimSpace(baseline) != "" {
		id, err = workspacebaseline.ID(baseline)
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
