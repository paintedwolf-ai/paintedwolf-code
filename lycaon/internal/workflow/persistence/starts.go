package persistence

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
	"strings"
	"time"
)

type Starts struct {
	transactions *Transactions
	runs         *Runs
}

func (s *Starts) ReplayStart(ctx context.Context, operationID, sessionID, inputDigest string) (*api.WorkflowRun, bool, error) {
	stored, err := s.transactions.queries.GetWorkflowStartOperation(ctx, operationID)
	if db.IsNoRows(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if stored.SessionID != sessionID || stored.InputDigest != inputDigest {
		return nil, false, fmt.Errorf("workflow start operation %s: %w", operationID, runstate.ErrOperationConflict)
	}
	var run api.WorkflowRun
	if err := json.Unmarshal([]byte(stored.ResponseJson), &run); err != nil {
		return nil, false, fmt.Errorf("decode workflow start receipt: %w", err)
	}
	return &run, true, nil
}

func (s *Starts) ActivateStart(ctx context.Context, operationID, inputDigest string, active, replacement *api.WorkflowRun, mutation runstate.StartMutation) ([]api.WorkflowRun, error) {
	if replacement == nil {
		return nil, fmt.Errorf("replacement workflow run required")
	}
	tx, err := s.transactions.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	queries := db.New(tx)
	var replaced []api.WorkflowRun
	// Rows wait for every run event below; see appendPlannedRowsTx.
	var supersededRows []api.Message
	if active != nil {
		replaced, err = s.runs.listBySession(ctx, queries, active.SessionID, 64, []string{
			string(api.WorkflowRunStatusRunning), string(api.WorkflowRunStatusPaused), string(api.WorkflowRunStatusPausedOnChild),
		})
		if err != nil {
			return nil, err
		}
		supersededVars := make(map[string]string, len(replaced))
		for i := range replaced {
			raw, varsErr := queries.GetScaffoldVars(ctx, replaced[i].ID)
			if varsErr != nil {
				return nil, varsErr
			}
			vars, varsErr := scaffoldVarsFromJSON(raw)
			if varsErr != nil {
				return nil, varsErr
			}
			if ask, ok := runstate.CoordinatorAskPendingFromVars(vars); ok {
				ask.State = runstate.CoordinatorAskSuperseded
				ask.ClosedReason = runstate.ExitReasonSupersededByWorkflowStart
				encoded, marshalErr := json.Marshal(runstate.SetCoordinatorAsk(vars, ask))
				if marshalErr != nil {
					return nil, marshalErr
				}
				supersededVars[replaced[i].ID] = string(encoded)
			}
		}
		now := time.Now().UTC()
		changed, cancelErr := queries.CancelActiveWorkflowLineage(ctx, db.CancelActiveWorkflowLineageParams{
			Reason: db.NullString(runstate.ExitReasonSupersededByWorkflowStart), CompletedAt: db.NullString(db.FormatTime(now)),
			UpdatedAt: db.FormatTime(now), TargetSessionID: active.SessionID, ExpectedID: active.ID, ExpectedRevision: active.Revision,
		})
		if cancelErr != nil {
			return nil, cancelErr
		}
		if changed == 0 {
			_ = tx.Rollback()
			if mutation.AllowReplacementRebase && mutation.ReplacementRebaseAttempts < 3 {
				current, currentErr := s.runs.ActiveBySession(ctx, active.SessionID)
				if currentErr != nil {
					return nil, currentErr
				}
				if current == nil || current.ID == active.ID {
					mutation.ReplacementRebaseAttempts++
					return s.ActivateStart(ctx, operationID, inputDigest, current, replacement, mutation)
				}
			}
			return nil, s.transactions.revisionConflict(ctx, active.ID, active.Revision)
		}
		for runID, varsJSON := range supersededVars {
			if err := queries.SetWorkflowRunVarsProjection(ctx, db.SetWorkflowRunVarsProjectionParams{
				VarsJson: varsJSON, ID: runID,
			}); err != nil {
				return nil, fmt.Errorf("mark superseded coordinator ask: %w", err)
			}
		}
		supersededRows, err = s.transactions.planCancellationBoundaries(ctx, tx, replaced, "", "canceled", runstate.ExitReasonSupersededByWorkflowStart)
		if err != nil {
			return nil, err
		}
		for i := range replaced {
			op, planned := mutation.ReplacementTeardowns[replaced[i].ID]
			if err := s.transactions.insertTreeTeardownTx(ctx, tx, &replaced[i], op, planned, runstate.ExitReasonSupersededByWorkflowStart); err != nil {
				return nil, err
			}
			canceled := replaced[i]
			canceled.Status = api.WorkflowRunStatusCanceled
			canceled.PauseReason = runstate.ExitReasonSupersededByWorkflowStart
			canceled.CompletedAt = &now
			canceled.UpdatedAt = now
			canceled.Revision++
			if err := s.transactions.enqueueRunTx(ctx, tx, &canceled, api.WorkflowEventKindRunCanceled, ""); err != nil {
				return nil, err
			}
		}
	}
	if err := s.transactions.createState(ctx, queries, replacement, mutation.ProjectDir, mutation.Vars); err != nil {
		return nil, err
	}
	if err := s.transactions.enqueueRunTx(ctx, tx, replacement, api.WorkflowEventKindRunStarted, ""); err != nil {
		return nil, err
	}
	// Superseded boundaries keep their place ahead of the new run's own rows.
	if err := s.transactions.appendPlannedRowsTx(ctx, tx, replacement.SessionID, supersededRows); err != nil {
		return nil, err
	}
	if err := s.transactions.appendPlannedRowsTx(ctx, tx, replacement.SessionID, mutation.Messages); err != nil {
		return nil, err
	}
	if mutation.Posture != "" {
		if s.transactions.sessions == nil {
			return nil, fmt.Errorf("workflow posture transaction participant unavailable")
		}
		if err := s.transactions.sessions.SetPostureTx(ctx, tx, replacement.SessionID, mutation.Posture); err != nil {
			return nil, err
		}
	}
	response, err := json.Marshal(replacement)
	if err != nil {
		return nil, err
	}
	if err := queries.InsertWorkflowStartOperation(ctx, db.InsertWorkflowStartOperationParams{
		ID: operationID, SessionID: replacement.SessionID, InputDigest: inputDigest,
		WorkflowRunID: replacement.ID, ResponseJson: string(response), CommittedAt: db.FormatTime(time.Now().UTC()),
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	if s.transactions.outbox != nil {
		s.transactions.outbox.Notify()
	}
	return replaced, nil
}

func (s *Starts) StartChild(ctx context.Context, parent, child *api.WorkflowRun, mutation runstate.ChildStartMutation) error {
	if parent == nil || child == nil {
		return fmt.Errorf("parent and child workflow runs required")
	}
	if parent.SessionID == "" || child.SessionID != parent.SessionID || child.ParentRunID == nil || strings.TrimSpace(*child.ParentRunID) != parent.ID {
		return fmt.Errorf("child must belong to and reference parent workflow run")
	}
	tx, err := s.transactions.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	queries := db.New(tx)
	failureJSON, err := db.MarshalJSON(parent.Failure)
	if err != nil {
		return fmt.Errorf("encode workflow failure: %w", err)
	}
	n, err := queries.UpdateWorkflowRun(ctx, db.UpdateWorkflowRunParams{
		Status:       string(parent.Status),
		CurrentPhase: parent.CurrentPhase,
		PauseReason:  db.NullString(parent.PauseReason),
		FailureJson:  failureJSON,
		UpdatedAt:    db.FormatTime(parent.UpdatedAt),
		PausedAt:     db.NullTimePtr(parent.PausedAt),
		CompletedAt:  db.NullTimePtr(parent.CompletedAt),
		ID:           parent.ID,
		Revision:     parent.Revision,
	})
	if err != nil {
		return err
	}
	if n == 0 {
		_ = tx.Rollback()
		return s.transactions.revisionConflict(ctx, parent.ID, parent.Revision)
	}
	if err := s.transactions.createState(ctx, queries, child, mutation.ProjectDir, mutation.Vars); err != nil {
		return err
	}
	parentNext := *parent
	parentNext.Revision++
	if err := s.transactions.enqueueRunTx(ctx, tx, &parentNext, api.WorkflowEventKindRunUpdated, ""); err != nil {
		return err
	}
	if err := s.transactions.enqueueRunTx(ctx, tx, child, api.WorkflowEventKindRunStarted, ""); err != nil {
		return err
	}
	if err := s.transactions.appendPlannedRowsTx(ctx, tx, parent.SessionID, mutation.Messages); err != nil {
		return err
	}
	if mutation.Posture != "" {
		if s.transactions.sessions == nil {
			return fmt.Errorf("workflow posture transaction participant unavailable")
		}
		if err := s.transactions.sessions.SetPostureTx(ctx, tx, parent.SessionID, mutation.Posture); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	*parent = parentNext
	if s.transactions.outbox != nil {
		s.transactions.outbox.Notify()
	}
	return nil
}
