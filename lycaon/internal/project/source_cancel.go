package project

import (
	"context"
	"errors"
	"os"
)

// A canceled preparation owns only its private stage and new recovery references.
func (s *SourceMutationService) cancelSourcePreparation(ctx context.Context, row *sourceMutationRow) error {
	plan := &row.Plan
	if plan.EffectStarted {
		return nil
	}
	if plan.StageIdentity != "" {
		if err := clearSourceStage(plan); err != nil && !os.IsNotExist(err) {
			return err
		}
		plan.StageIdentity = ""
	}
	if plan.RecoveryID == row.ID {
		if s.db != nil {
			tx, err := s.db.BeginTx(ctx, nil)
			if err != nil {
				return err
			}
			defer func() { _ = tx.Rollback() }()
			for _, query := range []string{
				`DELETE FROM source_recovery_entries WHERE project_id=? AND recovery_id=?`,
				`DELETE FROM source_recovery_objects WHERE project_id=? AND recovery_id=?`,
			} {
				if _, err := tx.ExecContext(ctx, query, plan.ProjectID, plan.RecoveryID); err != nil {
					return err
				}
			}
			if err := tx.Commit(); err != nil {
				return err
			}
		} else {
			s.stateMu.Lock()
			delete(s.recoveryEntries, plan.RecoveryID)
			s.stateMu.Unlock()
		}
		plan.RecoveryID = ""
		plan.RecoveryCount = 0
	}
	return s.update(ctx, row)
}

func (s *SourceMutationService) finishSourcePreparationFailure(ctx context.Context, row *sourceMutationRow, cause error) error {
	return errors.Join(cause, s.cancelSourcePreparation(context.WithoutCancel(ctx), row))
}
