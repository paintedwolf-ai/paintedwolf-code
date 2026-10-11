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

// reclaimRecoveryBatch releases at most one page from one queued manifest.
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
	var projectID, recoveryID string
	err = tx.QueryRowContext(ctx, `SELECT project_id,recovery_id FROM source_recovery_reclaim_queue ORDER BY project_id,recovery_id LIMIT 1`).Scan(&projectID, &recoveryID)
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
		for _, table := range []struct{ name, key string }{{"source_recovery_entries", "ordinal"}, {"source_recovery_objects", "sha256"}} {
			result, err := tx.ExecContext(ctx, `DELETE FROM `+table.name+` WHERE project_id=?1 AND recovery_id=?2 AND `+table.key+` IN (SELECT `+table.key+` FROM `+table.name+` WHERE project_id=?1 AND recovery_id=?2 ORDER BY `+table.key+` LIMIT ?3)`, projectID, recoveryID, blobMaintenanceBatchSize)
			if err != nil {
				return 0, err
			}
			n, err := result.RowsAffected()
			if err != nil {
				return 0, err
			}
			deleted += n
		}
	}
	// A full page may leave more entries for the next transaction.
	if claimed || deleted == 0 {
		if _, err := tx.ExecContext(ctx, `DELETE FROM source_recovery_reclaim_queue WHERE project_id=? AND recovery_id=?`, projectID, recoveryID); err != nil {
			return 0, err
		}
		deleted++
	}
	return deleted, tx.Commit()
}
