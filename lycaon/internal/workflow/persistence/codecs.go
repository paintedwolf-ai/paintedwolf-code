package persistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/pagecursor"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
)

func decodeWorkflowCommandReceipt(storedKind, storedDigest, response, rejectionJSON, kind, inputDigest string) (*api.WorkflowRun, bool, error) {
	if storedKind != kind || storedDigest != inputDigest {
		return nil, false, runstate.ErrRevisionConflict
	}
	var run api.WorkflowRun
	if err := json.Unmarshal([]byte(response), &run); err != nil {
		return nil, false, fmt.Errorf("decode workflow command receipt: %w", err)
	}
	if rejectionJSON != "" {
		var rejection runstate.CommandRejection
		if err := json.Unmarshal([]byte(rejectionJSON), &rejection); err != nil {
			return nil, false, fmt.Errorf("decode workflow command rejection: %w", err)
		}
		if rejection.Kind != "phase_gate_unmet" {
			return nil, false, fmt.Errorf("unknown workflow command rejection %q", rejection.Kind)
		}
		return &run, true, &runstate.PhaseGateUnmetError{
			Phase: rejection.Phase, Reason: rejection.Reason, FailedGate: rejection.FailedGate,
			FailedLeaves: append([]string(nil), rejection.Leaves...), Replayed: true,
		}
	}
	return &run, true, nil
}

func workflowMutationEvent(previousPhase, currentPhase string) (event api.WorkflowEventKind, previous string) {
	if previousPhase != currentPhase {
		return api.WorkflowEventKindPhaseAdvanced, previousPhase
	}
	return api.WorkflowEventKindRunUpdated, ""
}

func scaffoldVarsFromJSON(raw string) (map[string]any, error) {
	if strings.TrimSpace(raw) == "" {
		return map[string]any{}, nil
	}
	var vars map[string]any
	if err := json.Unmarshal([]byte(raw), &vars); err != nil {
		return nil, err
	}
	if vars == nil {
		return map[string]any{}, nil
	}
	return vars, nil
}

func parentRunID(id *string) string {
	if id == nil {
		return ""
	}
	return strings.TrimSpace(*id)
}

func runFromRow(r db.WorkflowRuns) (*api.WorkflowRun, error) {
	run := api.WorkflowRun{
		ID:              r.ID,
		SessionID:       r.SessionID,
		ProjectID:       r.ProjectID,
		WorkflowID:      r.WorkflowID,
		WorkflowVersion: r.WorkflowVersion,
		AttachPolicy:    r.AttachPolicy,
		Status:          api.WorkflowRunStatus(r.Status),
		Revision:        r.Revision,
		CurrentPhase:    r.CurrentPhase,
		BlueprintPath:   db.StringFromNull(r.BlueprintPath),
		PauseReason:     db.StringFromNull(r.PauseReason),
		StartMessageID:  db.StringFromNull(r.StartMessageID),
		EndMessageID:    db.StringFromNull(r.EndMessageID),
	}
	if pid := db.StringFromNull(r.ParentRunID); pid != "" {
		run.ParentRunID = &pid
	}
	if r.FailureJson.Valid {
		var failure api.WorkflowFailure
		if err := db.UnmarshalJSON(r.FailureJson, &failure); err != nil {
			return nil, fmt.Errorf("decode workflow failure: %w", err)
		}
		run.Failure = &failure
	}
	var err error
	run.CreatedAt, err = db.ParseTime(r.CreatedAt)
	if err != nil {
		return nil, err
	}
	run.UpdatedAt, err = db.ParseTime(r.UpdatedAt)
	if err != nil {
		return nil, err
	}
	run.PausedAt, err = db.TimePtrFromNull(r.PausedAt)
	if err != nil {
		return nil, err
	}
	run.CompletedAt, err = db.TimePtrFromNull(r.CompletedAt)
	return &run, err
}

func insertTeardownTx(ctx context.Context, tx *sql.Tx, op *runstate.TeardownIntent) error {
	if op == nil {
		return nil
	}
	now := db.FormatTime(time.Now().UTC())
	return db.New(tx).InsertWorkflowTeardownOperation(ctx, db.InsertWorkflowTeardownOperationParams{
		ID: op.ID, RunID: op.RunID, SourceRevision: op.SourceRevision,
		CancelScope: string(op.CancelScope), AbortDelegation: boolInt64(op.AbortDelegation),
		Reason: op.Reason, CreatedAt: now, UpdatedAt: now,
	})
}

func boolInt64(value bool) int64 {
	if value {
		return 1
	}
	return 0
}

func boundedRunPageLimit(limit int) int {
	if limit <= 0 {
		return 20
	}
	if limit > 100 {
		return 100
	}
	return limit
}

func normalizedRunStatuses(statuses []string) []string {
	out := make([]string, 0, len(statuses))
	seen := make(map[string]struct{}, len(statuses))
	for _, status := range statuses {
		if status = strings.TrimSpace(status); status != "" {
			if _, exists := seen[status]; exists {
				continue
			}
			seen[status] = struct{}{}
			out = append(out, status)
		}
	}
	sort.Strings(out)
	return out
}

func decodeWorkflowRunPageCursor(raw, sessionID string, statuses []string) (workflowRunPageCursor, error) {
	if strings.TrimSpace(raw) == "" {
		return workflowRunPageCursor{}, nil
	}
	cursor, err := workflowRunPages.Decode(raw, workflowRunPageScope(sessionID, statuses))
	if err != nil {
		return workflowRunPageCursor{}, fmt.Errorf("%w: %w", ErrInvalidRunPageCursor, err)
	}
	if cursor.WatermarkOrdinal <= 0 {
		return workflowRunPageCursor{}, fmt.Errorf("%w: %w", ErrInvalidRunPageCursor, pagecursor.ErrInvalid)
	}
	return cursor, nil
}

func encodeWorkflowRunPageCursor(cursor workflowRunPageCursor, sessionID string, statuses []string) (string, error) {
	return workflowRunPages.Encode(workflowRunPageScope(sessionID, statuses), cursor)
}

func workflowRunPageScope(sessionID string, statuses []string) string {
	return pagecursor.Scope(append([]string{sessionID}, statuses...)...)
}

func workflowRunsFromRows(rows []db.WorkflowRuns) ([]api.WorkflowRun, error) {
	out := make([]api.WorkflowRun, 0, len(rows))
	for _, row := range rows {
		run, err := runFromRow(row)
		if err != nil {
			return nil, err
		}
		out = append(out, *run)
	}
	return out, nil
}

func verdictOperationFromRow(row db.WorkflowVerdictOperations) (runstate.VerdictOperation, error) {
	createdAt, err := db.ParseTime(row.CreatedAt)
	if err != nil {
		return runstate.VerdictOperation{}, fmt.Errorf("parse verdict operation %s created_at: %w", row.ToolCallID, err)
	}
	return runstate.VerdictOperation{
		ToolCallID: row.ToolCallID, RunID: row.RunID, SourceRevision: row.SourceRevision,
		Phase: row.Phase, InputDigest: row.InputDigest, EvidenceRecordID: row.EvidenceRecordID,
		EvidenceJSON: row.EvidenceJson, Status: row.Status, ResponseJSON: row.ResponseJson.String,
		Error: row.Error, CreatedAt: createdAt,
	}, nil
}
