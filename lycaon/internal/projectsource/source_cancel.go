package projectsource

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
		if s.Journal.db == nil {
			s.recovery.stateMu.Lock()
			delete(s.recovery.recoveryEntries, plan.RecoveryID)
			s.recovery.stateMu.Unlock()
		}
		plan.RecoveryID = ""
		plan.RecoveryCount = 0
	}
	return s.Journal.update(ctx, row)
}

func (s *SourceMutationService) finishSourcePreparationFailure(ctx context.Context, row *sourceMutationRow, cause error) error {
	return errors.Join(cause, s.cancelSourcePreparation(context.WithoutCancel(ctx), row))
}
