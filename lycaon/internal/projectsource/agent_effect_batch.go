package projectsource

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/lycaon/lycaon/internal/sourceeffect"
)

type agentEffectBatch struct {
	service  *SourceMutationService
	pending  []*agentSourceMutation
	finished bool
}

// PrepareEffects durably admits every individual intent before publication.
// Filesystem effects run only after this transaction has closed.
func (s *SourceMutationService) PrepareEffects(ctx context.Context, effects []sourceeffect.Plan) (sourceeffect.PendingEffects, error) {
	batch := &agentEffectBatch{service: s}
	ready := false
	defer func() {
		if !ready {
			batch.release()
		}
	}()
	seen := make(map[string]bool, len(effects))
	for _, effect := range effects {
		if id := effect.Record.OperationID; id != "" {
			if seen[id] {
				return nil, ErrSourceMutationConflict
			}
			seen[id] = true
		}
		pending, err := s.prepareAgentEffect(ctx, effect)
		if err != nil {
			return nil, err
		}
		batch.pending = append(batch.pending, pending)
	}
	tx, err := s.Journal.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	for _, pending := range batch.pending {
		if err := s.Journal.insertWith(ctx, tx, pending.row); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	for _, pending := range batch.pending {
		notePreparedAgentEffect(pending.row)
	}
	ready = true
	return batch, nil
}

func (b *agentEffectBatch) release() {
	for _, pending := range b.pending {
		if pending.release != nil {
			pending.release()
			pending.release = nil
		}
	}
}

func (b *agentEffectBatch) IDs() []string {
	ids := make([]string, len(b.pending))
	for i, pending := range b.pending {
		ids[i] = pending.ID()
	}
	return ids
}

// Finish records definite non-effects separately from applied or uncertain
// publications. Matching pre-existing bytes never make an unattempted write.
func (b *agentEffectBatch) Finish(ctx context.Context, completions []sourceeffect.Completion) (resultErr error) {
	if b.finished {
		return fmt.Errorf("source effect batch already completed")
	}
	b.finished = true
	defer b.release()
	if len(completions) != len(b.pending) {
		return fmt.Errorf("source effect completion count differs from intents")
	}
	var appliedID string
	defer func() {
		if resultErr != nil && appliedID != "" {
			var applied *sourceeffect.AppliedError
			if !errors.As(resultErr, &applied) {
				resultErr = &sourceeffect.AppliedError{OperationID: appliedID, Cause: resultErr}
			}
		}
	}()
	ctx = context.WithoutCancel(ctx)
	applied := make([]*sourceMutationRow, 0, len(completions))
	var outcome error
	for i, completion := range completions {
		row := b.pending[i].row
		if appliedID == "" && completion.Attempted && completion.Result.Publication.Committed() {
			appliedID = row.ID
		}
		row.Error = ""
		if completion.Err != nil {
			row.Error = completion.Err.Error()
			outcome = errors.Join(outcome, completion.Err)
		}
		switch {
		case !completion.Attempted || !completion.Result.Publication.Committed():
			row.Status = sourceMutationFailed
		case completion.Err != nil && observeAgentEffect(row.Plan.AgentEffect) != nil:
			row.Status = sourceMutationDiverged
		default:
			row.Status = sourceMutationFileApplied
			row.Error = ""
			applied = append(applied, row)
		}
	}
	tx, err := b.service.Journal.db.BeginTx(ctx, nil)
	if err != nil {
		return errors.Join(outcome, err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, pending := range b.pending {
		pending.row.UpdatedAt = time.Now().UTC()
		if err := updateSourceMutation(ctx, tx, pending.row); err != nil {
			return errors.Join(outcome, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return errors.Join(outcome, err)
	}
	if err := b.service.settlement.commitRows(ctx, applied); err != nil {
		return errors.Join(outcome, err)
	}
	return outcome
}
