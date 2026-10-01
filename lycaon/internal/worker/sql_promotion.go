package worker

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/sourcefeed"
	"github.com/lycaon/lycaon/pkg/api"
)

// SetMergeStatus records coordinator merge state for a worker branch.
func (s *SQLStore) SetMergeStatus(ctx context.Context, jobID string, status api.WorkerMergeStatus) error {
	jobID = strings.TrimSpace(jobID)
	return s.inTx(ctx, func(q *db.Queries, tx *sql.Tx) error {
		n, err := q.SetWorkerJobMergeStatus(ctx, db.SetWorkerJobMergeStatusParams{
			MergeStatus: db.NullString(string(status)), ID: jobID,
		})
		if err != nil {
			return err
		}
		if n != 1 {
			return fmt.Errorf("worker %s not found for merge status update", jobID)
		}
		return EnqueueJobEventTx(ctx, tx, s.outbox, jobID)
	})
}

// SetMergeStatuses commits a related overlay state transition atomically.
func (s *SQLStore) SetMergeStatuses(ctx context.Context, updates []MergeStatusUpdate) error {
	return s.inTx(ctx, func(q *db.Queries, tx *sql.Tx) error {
		seen := make(map[string]struct{}, len(updates))
		for _, update := range updates {
			jobID := strings.TrimSpace(update.JobID)
			if jobID == "" {
				return fmt.Errorf("merge status job id required")
			}
			if _, exists := seen[jobID]; exists {
				return fmt.Errorf("duplicate merge status update for %s", jobID)
			}
			seen[jobID] = struct{}{}
			worker, err := q.GetWorkerJob(ctx, jobID)
			if err != nil {
				return err
			}
			n, err := q.SetLiveWorkerJobMergeStatus(ctx, db.SetLiveWorkerJobMergeStatusParams{
				MergeStatus: db.NullString(string(update.Status)), ID: jobID,
			})
			if err != nil {
				return err
			}
			if n != 1 {
				return fmt.Errorf("worker %s not found for merge status update", jobID)
			}
			if mergeStatusTerminal(update.Status) && worker.ParentSessionID.Valid {
				if err := q.DeleteAgentCallReservations(ctx, db.DeleteAgentCallReservationsParams{
					SessionID: worker.ParentSessionID, Agent: jobID,
				}); err != nil {
					return err
				}
			}
			if err := EnqueueJobEventTx(ctx, tx, s.outbox, jobID); err != nil {
				return err
			}
		}
		return nil
	})
}

// CommitPromotion finalizes one landed promotion under its lease.
func (s *SQLStore) CommitPromotion(ctx context.Context, jobID, claimToken string, commit PromotionCommit) error {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return nil
	}
	if s == nil || s.db == nil {
		return fmt.Errorf("worker store not configured")
	}
	plan := commit.Plan
	if !plan.Empty() {
		if strings.TrimSpace(plan.ID) == "" {
			return fmt.Errorf("landed change id required")
		}
		if plan.WorkerJobID != jobID {
			return fmt.Errorf("landed change worker %q does not match %q", plan.WorkerJobID, jobID)
		}
		if strings.TrimSpace(plan.CanonicalPath) == "" {
			return fmt.Errorf("landed change canonical path required")
		}
		if !plan.Required && strings.TrimSpace(plan.ScanID) != "" {
			return fmt.Errorf("optional landed change cannot carry a scan id")
		}
	}
	childIDs := make(map[string]struct{}, len(commit.ChildStatuses))
	for _, update := range commit.ChildStatuses {
		childID := strings.TrimSpace(update.JobID)
		if childID == "" || childID == jobID {
			return fmt.Errorf("invalid promotion child overlay %q", childID)
		}
		if _, duplicate := childIDs[childID]; duplicate {
			return fmt.Errorf("duplicate promotion child overlay %s", childID)
		}
		childIDs[childID] = struct{}{}
		if update.Status != api.WorkerMergeStatusPending && update.Status != api.WorkerMergeStatusRebasing {
			return fmt.Errorf("invalid promotion child status %q", update.Status)
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	worker, err := q.GetWorkerJob(ctx, jobID)
	if err != nil {
		return err
	}
	mergeStatus := api.WorkerMergeStatus(db.StringFromNull(worker.MergeStatus))
	if mergeStatus == api.WorkerMergeStatusMerged {
		if plan.Empty() {
			return nil
		}
		existingID, lookupErr := q.GetLandedChangeIDByWorkerJobID(ctx, jobID)
		if lookupErr != nil {
			return fmt.Errorf("merged worker %s is missing its landed change: %w", jobID, lookupErr)
		}
		if existingID != plan.ID {
			return fmt.Errorf("landed change identity mismatch for worker %s", jobID)
		}
		return nil
	}
	if mergeStatus != api.WorkerMergeStatusApplying || db.StringFromNull(worker.MergeClaimToken) != claimToken {
		return fmt.Errorf("worker %s is not applying under this claim", jobID)
	}
	if worker.ParentSessionID.Valid {
		if err := q.DeleteAgentCallReservations(ctx, db.DeleteAgentCallReservationsParams{
			SessionID: worker.ParentSessionID, Agent: jobID,
		}); err != nil {
			return err
		}
	}

	if !plan.Empty() {
		existingID, lookupErr := q.GetLandedChangeIDByWorkerJobID(ctx, jobID)
		switch {
		case lookupErr == nil:
			if existingID != plan.ID {
				return fmt.Errorf("landed change identity mismatch for worker %s", jobID)
			}
		case !db.IsNoRows(lookupErr):
			return lookupErr
		default:
			if err := insertLandedChangeTx(ctx, q, plan); err != nil {
				return err
			}
		}
	}
	if commit.Documents != nil {
		if err := commit.Documents.CommitTx(ctx, tx); err != nil {
			return fmt.Errorf("commit promoted editor documents: %w", err)
		}
	}
	if len(commit.Records) > 0 {
		if commit.Recorder == nil {
			return fmt.Errorf("promotion source recorder required")
		}
		if err := commit.Recorder.RecordBatchTx(ctx, tx, commit.Records); err != nil {
			return err
		}
	}
	delivery, err := sourcefeed.EmitBatchTx(ctx, tx, commit.Changes)
	if err != nil {
		return err
	}
	for _, update := range commit.ChildStatuses {
		n, err := q.SetLiveWorkerJobMergeStatus(ctx, db.SetLiveWorkerJobMergeStatusParams{
			MergeStatus: db.NullString(string(update.Status)), ID: strings.TrimSpace(update.JobID),
		})
		if err != nil {
			return err
		}
		if n != 1 {
			return fmt.Errorf("child overlay %s missing during promotion", update.JobID)
		}
		if err := EnqueueJobEventTx(ctx, tx, s.outbox, update.JobID); err != nil {
			return err
		}
	}

	n, err := q.SetWorkerJobMergedFromApplying(ctx, db.SetWorkerJobMergedFromApplyingParams{
		ID:         jobID,
		ClaimToken: db.NullString(claimToken),
	})
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("worker %s is not applying under this claim", jobID)
	}
	if err := EnqueueJobEventTx(ctx, tx, s.outbox, jobID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.notify()
	delivery.DeliverCommitted()
	return nil
}

func mergeStatusTerminal(status api.WorkerMergeStatus) bool {
	switch status {
	case api.WorkerMergeStatusMerged, api.WorkerMergeStatusRejected,
		api.WorkerMergeStatusOrphaned, api.WorkerMergeStatusAborted:
		return true
	default:
		return false
	}
}
