package projectsource

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/lycaon/lycaon/internal/fspath"
)

// Recover completes pending filesystem and ledger phases before requests are served.
func (s *SourceMutationService) Recover(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if err := s.recovery.pruneSourceRecovery(ctx, ""); err != nil {
		return err
	}
	rows, err := s.Journal.pending(ctx)
	if err != nil {
		return err
	}
	var recoveryErr error
	for _, row := range rows {
		if err := s.recoverOne(ctx, row); err != nil {
			recoveryErr = errors.Join(recoveryErr, fmt.Errorf("recover source mutation %s: %w", row.ID, err))
		}
	}
	return recoveryErr
}

func (s *SourceMutationService) recoverOne(ctx context.Context, row *sourceMutationRow) error {
	unlock, err := s.operationLocks.Acquire(ctx, row.ID)
	if err != nil {
		return err
	}
	defer unlock()
	// The row may change while recovery waits for the operation lock.
	current, found, err := s.Journal.load(ctx, row.ID)
	if err != nil {
		return err
	}
	if !found || (current.Status != sourceMutationPrepared && current.Status != sourceMutationFileApplied) {
		return nil
	}
	if current.Plan.AgentEffect != nil {
		return s.recoverAgentEffect(ctx, current)
	}
	if err := restoreSourcePublicationMode(&current.Plan); err != nil {
		current.Status, current.Error = sourceMutationFailed, err.Error()
		return s.Journal.update(ctx, current)
	}
	if current.Status == sourceMutationPrepared && current.Plan.CrossVolume && current.Plan.HoldStarted && sourceMutationPathExists(current.Plan.HoldAbs) {
		if err := s.Effects.publishSourceMove(ctx, current); err != nil {
			current.Status, current.Error = sourceMutationFailed, err.Error()
			return s.Journal.update(ctx, current)
		}
	}
	if current.Status == sourceMutationPrepared && sourceLifecycleKind(current.Plan.Kind) {
		applied := current.Plan.EffectStarted && verifySourceMutationApplied(ctx, &current.Plan) == nil
		if current.Plan.Kind == "delete" {
			identity, statErr := fspath.EntryIdentity(current.Plan.AbsPath)
			applied = current.Plan.DeleteStarted && (os.IsNotExist(statErr) || (statErr == nil && identity != current.Plan.DeleteIdentity))
		}
		if !applied {
			current.Status, current.Error = sourceMutationFailed, "File operation was interrupted before completion. Retry explicitly to continue."
			if err := s.cancelSourcePreparation(ctx, current); err != nil {
				current.Error = fmt.Sprintf("%s Temporary data cleanup: %v", current.Error, err)
			}
			return s.Journal.update(ctx, current)
		}
		current.Status = sourceMutationFileApplied
		if err := s.Journal.update(ctx, current); err != nil {
			return err
		}
		return s.settlement.commit(ctx, current)
	}
	_, err = s.resume(ctx, current)
	return err
}
