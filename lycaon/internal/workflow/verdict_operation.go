package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/pkg/api"
)

// VerdictSubmission is the journaled submit_verdict input.
type VerdictSubmission struct {
	SessionID string                               `json:"session_id"`
	Verdict   map[string]string                    `json:"verdict"`
	Cited     []api.CitationGroundingCitedEvidence `json:"cited_evidence"`
	CitedURLs []string                             `json:"cited_urls,omitempty"`
}

type verdictOperation struct {
	ToolCallID       string
	RunID            string
	SourceRevision   int64
	Phase            string
	InputDigest      string
	EvidenceRecordID string
	EvidenceJSON     string
	Status           string
	ResponseJSON     string
	Error            string
	// CreatedAt is when the verdict was submitted; recovery replays keep it.
	CreatedAt time.Time
}

// verdictOperationPending reports journal rows eligible for boot recovery.
func verdictOperationPending(status string) bool {
	return status == "prepared" || status == "evidence_applied"
}

func (s *SQLStore) getVerdictOperation(ctx context.Context, toolCallID string) (*verdictOperation, bool, error) {
	row, err := s.queries.GetWorkflowVerdictOperation(ctx, toolCallID)
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

func (s *SQLStore) prepareVerdictOperation(ctx context.Context, op verdictOperation) (*verdictOperation, bool, error) {
	now := db.FormatTime(time.Now().UTC())
	rows, err := s.queries.PrepareWorkflowVerdictOperation(ctx, db.PrepareWorkflowVerdictOperationParams{
		ToolCallID: op.ToolCallID, RunID: op.RunID, SourceRevision: op.SourceRevision,
		Phase: op.Phase, InputDigest: op.InputDigest, EvidenceRecordID: op.EvidenceRecordID,
		EvidenceJson: op.EvidenceJSON, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		return nil, false, err
	}
	stored, ok, err := s.getVerdictOperation(ctx, op.ToolCallID)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, fmt.Errorf("verdict operation %s missing after prepare", op.ToolCallID)
	}
	if stored.InputDigest != op.InputDigest {
		return nil, false, fmt.Errorf("submit_verdict tool call %s: %w", op.ToolCallID, ErrOperationConflict)
	}
	if stored.RunID != op.RunID || stored.SourceRevision != op.SourceRevision || stored.Phase != op.Phase {
		return nil, false, fmt.Errorf(
			"verdict operation %s was prepared for run %s phase %s revision %d and cannot apply to run %s phase %s revision %d",
			op.ToolCallID, stored.RunID, stored.Phase, stored.SourceRevision, op.RunID, op.Phase, op.SourceRevision)
	}
	return stored, rows == 1, nil
}

// resolveVerdictOperationDiverged takes the operation out of the boot recovery queue.
func (s *SQLStore) resolveVerdictOperationDiverged(ctx context.Context, toolCallID, reason string) error {
	rows, err := s.queries.ResolveWorkflowVerdictOperationDiverged(ctx, db.ResolveWorkflowVerdictOperationDivergedParams{
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

// rebaseVerdictOperation moves a prepared verdict onto a newer revision of the same phase.
func (s *SQLStore) rebaseVerdictOperation(ctx context.Context, toolCallID string, sourceRevision int64) error {
	_, err := s.queries.RebaseWorkflowVerdictOperation(ctx, db.RebaseWorkflowVerdictOperationParams{
		SourceRevision: sourceRevision, UpdatedAt: db.FormatTime(time.Now().UTC()), ToolCallID: toolCallID,
	})
	return err
}

func (s *SQLStore) markVerdictEvidenceApplied(ctx context.Context, toolCallID string) error {
	return s.queries.MarkWorkflowVerdictEvidenceApplied(ctx, db.MarkWorkflowVerdictEvidenceAppliedParams{
		UpdatedAt: db.FormatTime(time.Now().UTC()), ToolCallID: toolCallID,
	})
}

func (s *SQLStore) pendingVerdictOperations(ctx context.Context) ([]verdictOperation, error) {
	rows, err := s.queries.ListPendingWorkflowVerdictOperations(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]verdictOperation, 0, len(rows))
	for _, row := range rows {
		op, err := verdictOperationFromRow(row)
		if err != nil {
			return nil, err
		}
		out = append(out, op)
	}
	return out, nil
}

func verdictOperationFromRow(row db.WorkflowVerdictOperations) (verdictOperation, error) {
	createdAt, err := db.ParseTime(row.CreatedAt)
	if err != nil {
		return verdictOperation{}, fmt.Errorf("parse verdict operation %s created_at: %w", row.ToolCallID, err)
	}
	return verdictOperation{
		ToolCallID: row.ToolCallID, RunID: row.RunID, SourceRevision: row.SourceRevision,
		Phase: row.Phase, InputDigest: row.InputDigest, EvidenceRecordID: row.EvidenceRecordID,
		EvidenceJSON: row.EvidenceJson, Status: row.Status, ResponseJSON: row.ResponseJson.String,
		Error: row.Error, CreatedAt: createdAt,
	}, nil
}

// RecoverVerdictOperations replays pending verdicts; transient failures stay
// pending for the next boot.
func (m *RunManager) RecoverVerdictOperations(ctx context.Context) error {
	if m == nil || m.Store == nil {
		return nil
	}
	operations, err := m.Store.pendingVerdictOperations(ctx)
	if err != nil {
		return fmt.Errorf("list pending verdict operations: %w", err)
	}
	for _, op := range operations {
		var input VerdictSubmission
		if err := json.Unmarshal([]byte(op.EvidenceJSON), &input); err != nil {
			reason := "stored verdict input is invalid: " + err.Error()
			if resolveErr := m.Store.resolveVerdictOperationDiverged(ctx, op.ToolCallID, reason); resolveErr != nil {
				slog.ErrorContext(ctx, "workflow verdict operation could not be resolved",
					"tool_call_id", op.ToolCallID, "err", resolveErr)
			}
			continue
		}
		if _, err := m.RecordReviewLoopVerdict(withVerdictOperationID(ctx, op.ToolCallID), input.SessionID, input.Verdict, input.Cited, input.CitedURLs); err != nil {
			slog.WarnContext(ctx, "workflow verdict recovery deferred; row stays pending for the next boot",
				"tool_call_id", op.ToolCallID, "run_id", op.RunID, "phase", op.Phase, "err", err)
		}
	}
	return nil
}

func (s *SQLStore) commitVerdictOperation(ctx context.Context, op verdictOperation, run *api.WorkflowRun, projectDir string, vars map[string]any, outcome ReviewLoopVerdictOutcome) error {
	sourceRevision := run.Revision
	sourceUpdatedAt := run.UpdatedAt
	committed := false
	defer func() {
		if !committed {
			run.Revision = sourceRevision
			run.UpdatedAt = sourceUpdatedAt
		}
	}()
	tx, err := s.db.BeginTx(ctx, nil)
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
			return s.revisionConflict(ctx, run.ID, op.SourceRevision)
		}
		run.Revision++
		run.UpdatedAt = now
		if err := s.enqueueRunTx(ctx, tx, run, api.WorkflowEventKindRunUpdated, ""); err != nil {
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
	if s.outbox != nil {
		s.outbox.Notify()
	}
	return nil
}
