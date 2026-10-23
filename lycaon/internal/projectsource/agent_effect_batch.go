package projectsource

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/lycaon/lycaon/internal/sourceeffect"
	"github.com/lycaon/lycaon/internal/sourceledger"
)

type agentEffectBatch struct {
	service       *SourceMutationService
	pending       []*agentSourceMutation
	recorded      bool
	closed        bool
	releaseMemory func()
	prepared      []preparedSettlement
	appliedID     string
}

// PrepareEffects durably admits every individual intent before publication.
// Filesystem effects run only after this transaction has closed.
func (s *SourceMutationService) PrepareEffects(ctx context.Context, memory sourceeffect.Memory, effects []sourceeffect.Plan) (sourceeffect.PendingEffects, error) {
	if memory == nil {
		return nil, fmt.Errorf("source operation memory is required")
	}
	release, err := memory.ReserveProgramState(ctx, effectBatchStateBytes(effects))
	if err != nil {
		return nil, err
	}
	batch := &agentEffectBatch{service: s, releaseMemory: release}
	ready := false
	defer func() {
		if !ready {
			batch.Close()
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
		pending.row.memory = memory
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

// Close releases transient leases; it does not erase durable completion facts.
func (b *agentEffectBatch) Close() {
	if b.closed {
		return
	}
	b.closed = true
	closePreparedSettlements(b.prepared)
	b.prepared = nil
	for _, pending := range b.pending {
		if pending.release != nil {
			pending.release()
			pending.release = nil
		}
	}
	b.pending = nil
	if b.releaseMemory != nil {
		b.releaseMemory()
		b.releaseMemory = nil
	}
}

func (b *agentEffectBatch) IDs() []string {
	ids := make([]string, len(b.pending))
	for i, pending := range b.pending {
		ids[i] = pending.ID()
	}
	return ids
}

// Record makes known outcomes durable before preparing immutable attribution.
// Publication errors stay with the caller; this reports recording failures.
func (b *agentEffectBatch) Record(ctx context.Context, completions []sourceeffect.Completion) (resultErr error) {
	if b.recorded || b.closed {
		return ErrSourceMutationConflict
	}
	ready := false
	defer func() {
		if !ready {
			b.Close()
		}
	}()
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
	for i, completion := range completions {
		row := b.pending[i].row
		if appliedID == "" && completion.Attempted && completion.Result.Publication.Committed() {
			appliedID = row.ID
		}
		row.Error = ""
		if completion.Err != nil {
			row.Error = completion.Err.Error()
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
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, pending := range b.pending {
		pending.row.UpdatedAt = time.Now().UTC()
		if err := updateSourceMutation(ctx, tx, pending.row); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	prepared, err := b.service.settlement.prepareRows(ctx, applied)
	if err != nil {
		return err
	}
	b.prepared, b.appliedID = prepared, appliedID
	// The immutable recorder owns its clones. The durable file_applied plan
	// still contains the original bodies for restart recovery.
	for _, pending := range b.pending {
		effect := pending.row.Plan.AgentEffect
		effect.Record.Before, effect.Record.After = nil, nil
	}
	b.recorded, ready = true, true
	return nil
}

// SettleEffects commits bounded, already-applied pages in one ledger/event
// transaction. Its temporary vector is covered by each page's prepaid metadata.
func (s *SourceMutationService) SettleEffects(ctx context.Context, effects []sourceeffect.PendingEffects) (result error) {
	count := 0
	var appliedID string
	for i, effect := range effects {
		batch, ok := effect.(*agentEffectBatch)
		if !ok || batch.service != s || !batch.recorded || batch.closed {
			return ErrSourceMutationConflict
		}
		for _, previous := range effects[:i] {
			if previous.(*agentEffectBatch) == batch {
				return ErrSourceMutationConflict
			}
		}
		count += len(batch.prepared)
		if appliedID == "" {
			appliedID = batch.appliedID
		}
	}
	defer func() {
		for _, effect := range effects {
			effect.Close()
		}
		if result != nil && appliedID != "" {
			var applied *sourceeffect.AppliedError
			if !errors.As(result, &applied) {
				result = &sourceeffect.AppliedError{OperationID: appliedID, Cause: result}
			}
		}
	}()
	prepared := make([]preparedSettlement, 0, count)
	for _, effect := range effects {
		prepared = append(prepared, effect.(*agentEffectBatch).prepared...)
	}
	ctx = context.WithoutCancel(ctx)
	deliveries, err := s.settlement.commitPreparedRows(ctx, prepared)
	if err != nil {
		return err
	}
	for i, item := range prepared {
		deliveries[i].DeliverCommitted()
		notifySourceMutation(ctx, item.row.Plan)
	}
	return nil
}

// The fixed allowance covers intent JSON, pending records and overlapping
// settlement vectors; captured bodies and text spans have separate size terms.
func effectBatchStateBytes(effects []sourceeffect.Plan) int64 {
	bound := int64(4096)
	for _, effect := range effects {
		r, c := effect.Record, effect.Change
		bound += 16<<10 + 6*int64(len(r.Before)+len(r.After))
		for _, value := range []string{r.RootID, r.Path, r.FromRootID, r.FromPath, r.EntryKind, r.ProjectID, r.BranchID.String(), r.FileID, r.DerivedFromVersionID, string(r.Op), string(r.Origin), r.PersonID, r.SessionID, r.JobID, r.ToolCallID, r.ToolName, r.BatchID, r.BeforeSHA256, r.AfterSHA256, r.OperationID, r.Cause, r.CaptureQuality, r.ActorLabel, r.GitTransitionID, r.CommandWindowID, c.ProjectID, c.WorkspaceID, string(c.WorkspaceKind), c.RootID, c.Path, c.FromPath, string(c.Op), string(c.Origin), c.SessionID, c.JobID, c.ToolCallID, c.AfterSHA256, c.AbsPath, c.FromAbsPath, effect.Target.Root, effect.Target.Rel, effect.From.Root, effect.From.Rel} {
			bound += 8 * int64(len(value))
		}
		for _, state := range []*sourceledger.TextState{r.TextBefore, r.TextAfter} {
			if state != nil {
				bound += 128*int64(len(state.Spans)) + 8*int64(len(state.DocumentID))
			}
		}
		if r.NativeRecovery != nil {
			bound += 8 * int64(len(r.NativeRecovery.Key))
		}
	}
	return bound
}
