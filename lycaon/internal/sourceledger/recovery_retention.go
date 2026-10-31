package sourceledger

import (
	"context"
	"database/sql"
	"errors"
)

// Active receipts retain captures named by their ID before plan publication.
const recoveryClaimQuery = `SELECT
 EXISTS(SELECT 1 FROM source_history_entries WHERE project_id=?1 AND json_extract(undo_plan_json,'$.recovery_id')=?2)
 OR EXISTS(SELECT 1 FROM source_history_entries WHERE project_id=?1 AND json_extract(redo_plan_json,'$.recovery_id')=?2)
 OR EXISTS(SELECT 1 FROM source_mutations WHERE project_id=?1 AND status IN ('prepared','file_applied','failed','diverged') AND json_extract(NULLIF(plan_json,''),'$.recovery_id')=?2)
 OR EXISTS(SELECT 1 FROM source_mutations WHERE project_id=?1 AND id=?2 AND status IN ('prepared','file_applied'))`

// HoldRecoveryReferences protects preparation until its references commit.
func (s *Retention) HoldRecoveryReferences() func() {
	return s.objects.AcquireReferenceLease()
}

// reclaimRecoveryBatch bounds manifest rows and retired candidates in one transaction.
func (s *Retention) reclaimRecoveryBatch(ctx context.Context) (int64, error) {
	release, ok, err := s.objects.TryAcquireMaintenanceLease()
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, ErrBlobMaintenanceDeferred
	}
	defer release()
	tx, err := s.sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	// Empty candidates have no manifest bytes to release. Insert triggers queue
	// any later capture, including one whose operation identity is already live.
	result, err := tx.ExecContext(ctx, `WITH candidates AS MATERIALIZED (
		SELECT project_id,recovery_id FROM source_recovery_reclaim_queue
		ORDER BY project_id,recovery_id LIMIT ?
	)
	DELETE FROM source_recovery_reclaim_queue
	WHERE (project_id,recovery_id) IN (
		SELECT c.project_id,c.recovery_id FROM candidates c
		WHERE NOT EXISTS (SELECT 1 FROM source_recovery_entries e WHERE e.project_id=c.project_id AND e.recovery_id=c.recovery_id)
		AND NOT EXISTS (SELECT 1 FROM source_recovery_objects o WHERE o.project_id=c.project_id AND o.recovery_id=c.recovery_id)
	)`, blobMaintenanceBatchSize)
	if err != nil {
		return 0, err
	}
	reclaimed, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	for reclaimed < blobMaintenanceBatchSize {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		n, err := reclaimRecoveryCandidate(ctx, tx, blobMaintenanceBatchSize-reclaimed)
		if err != nil {
			return 0, err
		}
		if n == 0 {
			break
		}
		reclaimed += n
	}
	return reclaimed, tx.Commit()
}

// Candidate checks and retirement share the batch's writer transaction and lease.
func reclaimRecoveryCandidate(ctx context.Context, tx *sql.Tx, remaining int64) (int64, error) {
	var projectID, recoveryID string
	err := tx.QueryRowContext(ctx, `SELECT project_id,recovery_id FROM source_recovery_reclaim_queue ORDER BY project_id,recovery_id LIMIT 1`).Scan(&projectID, &recoveryID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var claimed bool
	if err := tx.QueryRowContext(ctx, recoveryClaimQuery, projectID, recoveryID).Scan(&claimed); err != nil {
		return 0, err
	}
	var deleted int64
	if !claimed {
		for _, query := range []string{
			`DELETE FROM source_recovery_entries WHERE project_id=?1 AND recovery_id=?2 AND ordinal IN (SELECT ordinal FROM source_recovery_entries WHERE project_id=?1 AND recovery_id=?2 ORDER BY ordinal LIMIT ?3)`,
			`DELETE FROM source_recovery_objects WHERE project_id=?1 AND recovery_id=?2 AND sha256 IN (SELECT sha256 FROM source_recovery_objects WHERE project_id=?1 AND recovery_id=?2 ORDER BY sha256 LIMIT ?3)`,
		} {
			result, err := tx.ExecContext(ctx, query, projectID, recoveryID, remaining-deleted)
			if err != nil {
				return 0, err
			}
			n, err := result.RowsAffected()
			if err != nil {
				return 0, err
			}
			deleted += n
			if deleted == remaining {
				break
			}
		}
	}
	// A full page may leave more entries for the next transaction.
	if claimed || deleted == 0 {
		if _, err := tx.ExecContext(ctx, `DELETE FROM source_recovery_reclaim_queue WHERE project_id=? AND recovery_id=?`, projectID, recoveryID); err != nil {
			return 0, err
		}
		deleted++
	}
	return deleted, nil
}
