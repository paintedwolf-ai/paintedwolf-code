package persistence

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
)

type Verdicts struct {
	transactions *Transactions
}

func (s *Verdicts) GetVerdictOperation(ctx context.Context, toolCallID string) (*runstate.VerdictOperation, bool, error) {
	row, err := s.transactions.queries.GetWorkflowVerdictOperation(ctx, toolCallID)
	if db.IsNoRows(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	op, err := verdictOperationFromRow(row)
	if err != nil {
		return nil, false, err
	}
	return &op, true, nil
}

func (s *Verdicts) PrepareVerdictOperation(ctx context.Context, op runstate.VerdictOperation) (*runstate.VerdictOperation, bool, error) {
	now := db.FormatTime(time.Now().UTC())
	rows, err := s.transactions.queries.PrepareWorkflowVerdictOperation(ctx, db.PrepareWorkflowVerdictOperationParams{
		ToolCallID: op.ToolCallID, RunID: op.RunID, SourceRevision: op.SourceRevision,
		Phase: op.Phase, InputDigest: op.InputDigest, EvidenceRecordID: op.EvidenceRecordID,
		EvidenceJson: op.EvidenceJSON, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		return nil, false, err
	}
	stored, ok, err := s.GetVerdictOperation(ctx, op.ToolCallID)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, fmt.Errorf("verdict operation %s missing after prepare", op.ToolCallID)
	}
	if stored.InputDigest != op.InputDigest {
		return nil, false, fmt.Errorf("submit_verdict tool call %s: %w", op.ToolCallID, runstate.ErrOperationConflict)
	}
	if stored.RunID != op.RunID || stored.SourceRevision != op.SourceRevision || stored.Phase != op.Phase {
		return nil, false, fmt.Errorf(
			"verdict operation %s was prepared for run %s phase %s revision %d and cannot apply to run %s phase %s revision %d",
			op.ToolCallID, stored.RunID, stored.Phase, stored.SourceRevision, op.RunID, op.Phase, op.SourceRevision)
	}
	return stored, rows == 1, nil
}

func (s *Verdicts) ResolveVerdictOperationDiverged(ctx context.Context, toolCallID, reason string) error {
	rows, err := s.transactions.queries.ResolveWorkflowVerdictOperationDiverged(ctx, db.ResolveWorkflowVerdictOperationDivergedParams{
		Error: reason, UpdatedAt: db.FormatTime(time.Now().UTC()), ToolCallID: toolCallID,
	})
	if err != nil {
		return err
	}
	if rows == 1 {
		slog.WarnContext(ctx, "workflow verdict operation diverged; terminally resolved",
			"tool_call_id", toolCallID, "reason", reason)
	}
	return nil
}

func (s *Verdicts) RebaseVerdictOperation(ctx context.Context, toolCallID string, sourceRevision int64) error {
	_, err := s.transactions.queries.RebaseWorkflowVerdictOperation(ctx, db.RebaseWorkflowVerdictOperationParams{
		SourceRevision: sourceRevision, UpdatedAt: db.FormatTime(time.Now().UTC()), ToolCallID: toolCallID,
	})
	return err
}

func (s *Verdicts) MarkVerdictEvidenceApplied(ctx context.Context, toolCallID string) error {
	return s.transactions.queries.MarkWorkflowVerdictEvidenceApplied(ctx, db.MarkWorkflowVerdictEvidenceAppliedParams{
		UpdatedAt: db.FormatTime(time.Now().UTC()), ToolCallID: toolCallID,
	})
}

func (s *Verdicts) PendingVerdictOperations(ctx context.Context) ([]runstate.VerdictOperation, error) {
	rows, err := s.transactions.queries.ListPendingWorkflowVerdictOperations(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]runstate.VerdictOperation, 0, len(rows))
	for _, row := range rows {
		op, err := verdictOperationFromRow(row)
		if err != nil {
			return nil, err
		}
		out = append(out, op)
	}
	return out, nil
}

func (s *Verdicts) CommitVerdictOperation(ctx context.Context, op runstate.VerdictOperation, run *api.WorkflowRun, projectDir string, vars map[string]any, outcome runstate.ReviewOutcome) error {
	sourceRevision := run.Revision
	sourceUpdatedAt := run.UpdatedAt
	committed := false
	defer func() {
		if !committed {
			run.Revision = sourceRevision
			run.UpdatedAt = sourceUpdatedAt
		}
	}()
	tx, err := s.transactions.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if vars != nil {
		now := time.Now().UTC()
		raw, marshalErr := json.Marshal(vars)
		if marshalErr != nil {
			return marshalErr
		}
		changed, updateErr := db.New(tx).UpdateWorkflowRunVars(ctx, db.UpdateWorkflowRunVarsParams{
			ProjectDir: projectDir, VarsJson: string(raw), UpdatedAt: db.FormatTime(now),
			ID: run.ID, Revision: op.SourceRevision,
		})
		if updateErr != nil {
			return updateErr
		}
		if changed == 0 {
			return s.transactions.revisionConflict(ctx, run.ID, op.SourceRevision)
		}
		run.Revision++
		run.UpdatedAt = now
		if err := s.transactions.enqueueRunTx(ctx, tx, run, api.WorkflowEventKindRunUpdated, ""); err != nil {
			return err
		}
	}
	response, err := json.Marshal(outcome)
	if err != nil {
		return err
	}
	rows, err := db.New(tx).CommitWorkflowVerdictOperation(ctx, db.CommitWorkflowVerdictOperationParams{
		ResponseJson: db.NullString(string(response)), UpdatedAt: db.FormatTime(time.Now().UTC()), ToolCallID: op.ToolCallID,
	})
	if err != nil {
		return err
	}
	if rows != 1 {
		return fmt.Errorf("verdict operation %s is not recoverable", op.ToolCallID)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	if s.transactions.outbox != nil {
		s.transactions.outbox.Notify()
	}
	return nil
}
