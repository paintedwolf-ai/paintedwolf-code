package worker

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/db"
)

// mergeApplyLease bounds promotion heartbeat gaps.
const mergeApplyLease = 45 * time.Second

// BeginMergeApply leases a pending branch.
func (s *SQLStore) BeginMergeApply(ctx context.Context, jobID string) (string, bool, error) {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return "", false, nil
	}
	token := uuid.NewString()
	now := time.Now().UTC()
	won, err := s.casInTx(ctx, jobID, func(q *db.Queries) (int64, error) {
		return q.BeginWorkerJobMergeApply(ctx, db.BeginWorkerJobMergeApplyParams{
			ClaimToken:     db.NullString(token),
			HeartbeatAt:    db.NullString(db.FormatTime(now)),
			LeaseExpiresAt: db.NullString(db.FormatTime(now.Add(mergeApplyLease))),
			ID:             jobID,
		})
	})
	if err != nil || !won {
		return "", false, err
	}
	return token, true, nil
}

// RenewMergeApply extends the merge-apply lease while the apply is live.
func (s *SQLStore) RenewMergeApply(ctx context.Context, jobID, claimToken string) (bool, error) {
	now := time.Now().UTC()
	n, err := s.queries.RenewWorkerJobMergeClaim(ctx, db.RenewWorkerJobMergeClaimParams{
		HeartbeatAt:    db.NullString(db.FormatTime(now)),
		LeaseExpiresAt: db.NullString(db.FormatTime(now.Add(mergeApplyLease))),
		ID:             jobID,
		ClaimToken:     db.NullString(claimToken),
	})
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

// ReleaseMergeApply returns an applying branch to pending under its claim.
func (s *SQLStore) ReleaseMergeApply(ctx context.Context, jobID, claimToken string) error {
	jobID = strings.TrimSpace(jobID)
	return s.mutateInTx(ctx, jobID, func(q *db.Queries) error {
		n, err := q.ReleaseWorkerJobMergeApply(ctx, db.ReleaseWorkerJobMergeApplyParams{
			ID:         jobID,
			ClaimToken: db.NullString(claimToken),
		})
		if err != nil {
			return err
		}
		if n != 1 {
			return errMergeApplyClaimLost
		}
		return nil
	})
}

// ReclaimExpiredMergeApply takes over an expired apply lease.
func (s *SQLStore) ReclaimExpiredMergeApply(ctx context.Context, jobID string) (string, bool, error) {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return "", false, nil
	}
	token := uuid.NewString()
	now := time.Now().UTC()
	won, err := s.casInTx(ctx, jobID, func(q *db.Queries) (int64, error) {
		return q.ReclaimExpiredWorkerJobMergeApply(ctx, db.ReclaimExpiredWorkerJobMergeApplyParams{
			ClaimToken:     db.NullString(token),
			HeartbeatAt:    db.NullString(db.FormatTime(now)),
			LeaseExpiresAt: db.NullString(db.FormatTime(now.Add(mergeApplyLease))),
			ID:             jobID,
			ExpiredBefore:  db.NullString(db.FormatTime(now)),
		})
	})
	if err != nil || !won {
		return "", false, err
	}
	return token, true, nil
}

// ListMergeApplying returns job ids whose merge status is applying.
func (s *SQLStore) ListMergeApplying(ctx context.Context) ([]string, error) {
	return s.queries.ListMergeApplyingWorkerJobIDs(ctx)
}
