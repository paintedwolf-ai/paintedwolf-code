package scan

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/pkg/api"
)

// BindContexts records the contexts using reusable evidence.
func (s *SQLStore) BindContexts(ctx context.Context, id, assessmentID, workflowRunID, sessionID string) (*api.CodeScan, error) {
	if s == nil {
		return nil, fmt.Errorf("scan store not configured")
	}
	id = strings.TrimSpace(id)
	assessmentID = strings.TrimSpace(assessmentID)
	workflowRunID = strings.TrimSpace(workflowRunID)
	sessionID = strings.TrimSpace(sessionID)
	if id == "" || (assessmentID == "" && workflowRunID == "" && sessionID == "") {
		return nil, fmt.Errorf("bind scan contexts requires id and an assessment, workflow, or session")
	}
	err := s.inTx(ctx, func(qtx *db.Queries, tx *sql.Tx) error {
		if _, err := qtx.GetCodeScan(ctx, id); err != nil {
			if db.IsNoRows(err) {
				return fmt.Errorf("scan %s not found", id)
			}
			return err
		}
		changed, err := bindScanContextsTx(ctx, qtx, id, assessmentID, workflowRunID, sessionID, time.Now().UTC())
		if err != nil {
			return err
		}
		if !changed {
			return nil
		}
		return s.emitScanTx(ctx, tx, id)
	})
	if err != nil {
		return nil, err
	}
	row, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, fmt.Errorf("scan %s not found", id)
	}
	row.AssessmentID = assessmentID
	row.WorkflowRunID = workflowRunID
	row.SessionID = sessionID
	return row, nil
}

// TerminalWorkflowBinding identifies one workflow refresh awaiting delivery.
type TerminalWorkflowBinding struct {
	WorkflowRunID string
	ScanID        string
}

// PendingTerminalWorkflowBindings lists unacknowledged terminal deliveries.
func (s *SQLStore) PendingTerminalWorkflowBindings(ctx context.Context, scanID string, limit int) ([]TerminalWorkflowBinding, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 256
	}
	rows, err := s.queries.ListPendingTerminalWorkflowScanBindings(ctx, db.ListPendingTerminalWorkflowScanBindingsParams{
		ScanID: strings.TrimSpace(scanID), BindingLimit: int64(limit),
	})
	if err != nil {
		return nil, err
	}
	out := make([]TerminalWorkflowBinding, len(rows))
	for i := range rows {
		out[i] = TerminalWorkflowBinding{WorkflowRunID: rows[i].WorkflowRunID, ScanID: rows[i].ScanID}
	}
	return out, nil
}

// BindWorkflowRun binds an existing scan to a workflow run, so the run's
// inventory and coverage include it.
func (s *SQLStore) BindWorkflowRun(ctx context.Context, scanID, workflowRunID string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("scan store not configured")
	}
	return s.inTx(ctx, func(qtx *db.Queries, tx *sql.Tx) error {
		changed, err := bindScanContextsTx(ctx, qtx, strings.TrimSpace(scanID), "", workflowRunID, "", time.Now().UTC())
		if err != nil || !changed {
			return err
		}
		return s.emitScanTx(ctx, tx, strings.TrimSpace(scanID))
	})
}

// MarkWorkflowTerminalNotified acknowledges a terminal delivery.
func (s *SQLStore) MarkWorkflowTerminalNotified(ctx context.Context, scanID, workflowRunID string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("scan store not configured")
	}
	return s.queries.MarkWorkflowScanTerminalNotified(ctx, db.MarkWorkflowScanTerminalNotifiedParams{
		TerminalNotifiedAt: db.NullString(db.FormatTime(time.Now().UTC())),
		WorkflowRunID:      strings.TrimSpace(workflowRunID), ScanID: strings.TrimSpace(scanID),
	})
}

func bindScanContextsTx(ctx context.Context, qtx *db.Queries, scanID, assessmentID, workflowRunID, sessionID string, at time.Time) (bool, error) {
	createdAt := db.FormatTime(at)
	changed := false
	if assessmentID = strings.TrimSpace(assessmentID); assessmentID != "" {
		rows, err := qtx.BindScanAssessment(ctx, db.BindScanAssessmentParams{
			AssessmentID: assessmentID, ScanID: scanID, CreatedAt: createdAt,
		})
		if err != nil {
			return false, err
		}
		changed = rows > 0
	}
	if workflowRunID = strings.TrimSpace(workflowRunID); workflowRunID != "" {
		rows, err := qtx.BindScanWorkflowRun(ctx, db.BindScanWorkflowRunParams{
			WorkflowRunID: workflowRunID, ScanID: scanID, CreatedAt: createdAt,
		})
		if err != nil {
			return false, err
		}
		changed = changed || rows > 0
	}
	if sessionID = strings.TrimSpace(sessionID); sessionID != "" {
		rows, err := qtx.BindScanSession(ctx, db.BindScanSessionParams{
			SessionID: sessionID, ScanID: scanID, CreatedAt: createdAt,
		})
		if err != nil {
			return false, err
		}
		changed = changed || rows > 0
	}
	return changed, nil
}
