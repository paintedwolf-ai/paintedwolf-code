package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/eventoutbox"
	"github.com/lycaon/lycaon/internal/people"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
)

// SQLStore implements RunStore against SQLite.
type SQLStore struct {
	db       db.Handle
	queries  *db.Queries
	outbox   *eventoutbox.Outbox
	sessions atomicSessionMutations
	// Commit blueprint approvals and their authorization records together.
	authz   authzledger.TransactionalRecorder
	workers worker.RunnableNotifier
}

type atomicSessionMutations interface {
	AppendMessagesTx(ctx context.Context, tx *sql.Tx, sessionID string, msgs ...api.Message) error
	SetPostureTx(ctx context.Context, tx *sql.Tx, sessionID string, posture api.SessionPosture) error
}

// NewSQLStore creates a workflow run store.
func NewSQLStore(database db.Handle) *SQLStore {
	return &SQLStore{db: database, queries: db.New(database)}
}

func (s *SQLStore) SetEventOutbox(outbox *eventoutbox.Outbox) {
	s.outbox = outbox
}

// SetWorkerRunnableNotifier wires the post-commit worker claimer wake.
func (s *SQLStore) SetWorkerRunnableNotifier(notifier worker.RunnableNotifier) {
	s.workers = notifier
}

// MutationEventsOutboxed reports whether mutations commit their events.
func (s *SQLStore) MutationEventsOutboxed() bool {
	return s != nil && s.outbox != nil
}

func (s *SQLStore) SetSessionMutations(store atomicSessionMutations) {
	s.sessions = store
}

// SetAuthzRecorder wires tamper-evident blueprint approval recording.
func (s *SQLStore) SetAuthzRecorder(rec authzledger.TransactionalRecorder) {
	s.authz = rec
}

func (s *SQLStore) ReplayStart(ctx context.Context, operationID, sessionID, inputDigest string) (*api.WorkflowRun, bool, error) {
	stored, err := s.queries.GetWorkflowStartOperation(ctx, operationID)
	if db.IsNoRows(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if stored.SessionID != sessionID || stored.InputDigest != inputDigest {
		return nil, false, fmt.Errorf("workflow start operation %s: %w", operationID, ErrOperationConflict)
	}
	var run api.WorkflowRun
	if err := json.Unmarshal([]byte(stored.ResponseJson), &run); err != nil {
		return nil, false, fmt.Errorf("decode workflow start receipt: %w", err)
	}
	return &run, true, nil
}

func (s *SQLStore) ActivateStart(ctx context.Context, operationID, inputDigest string, active, replacement *api.WorkflowRun, mutation workflowStartMutation) ([]api.WorkflowRun, error) {
	if replacement == nil {
		return nil, fmt.Errorf("replacement workflow run required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	queries := db.New(tx)
	var replaced []api.WorkflowRun
	// Rows wait for every run event below; see appendPlannedRowsTx.
	var supersededRows []api.Message
	if active != nil {
		replaced, err = s.listBySession(ctx, queries, active.SessionID, 64, []string{
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
			if ask, ok := coordinatorAskPendingFromVars(vars); ok {
				ask.State = coordinatorAskSuperseded
				ask.ClosedReason = exitReasonSupersededByWorkflowStart
				encoded, marshalErr := json.Marshal(setCoordinatorAsk(vars, ask))
				if marshalErr != nil {
					return nil, marshalErr
				}
				supersededVars[replaced[i].ID] = string(encoded)
			}
		}
		now := time.Now().UTC()
		changed, cancelErr := queries.CancelActiveWorkflowLineage(ctx, db.CancelActiveWorkflowLineageParams{
			Reason: db.NullString(exitReasonSupersededByWorkflowStart), CompletedAt: db.NullString(db.FormatTime(now)),
			UpdatedAt: db.FormatTime(now), TargetSessionID: active.SessionID, ExpectedID: active.ID, ExpectedRevision: active.Revision,
		})
		if cancelErr != nil {
			return nil, cancelErr
		}
		if changed == 0 {
			_ = tx.Rollback()
			if mutation.AllowReplacementRebase && mutation.replacementRebaseAttempts < 3 {
				current, currentErr := s.ActiveBySession(ctx, active.SessionID)
				if currentErr != nil {
					return nil, currentErr
				}
				if current == nil || current.ID == active.ID {
					mutation.replacementRebaseAttempts++
					return s.ActivateStart(ctx, operationID, inputDigest, current, replacement, mutation)
				}
			}
			return nil, s.revisionConflict(ctx, active.ID, active.Revision)
		}
		for runID, varsJSON := range supersededVars {
			if err := queries.SetWorkflowRunVarsProjection(ctx, db.SetWorkflowRunVarsProjectionParams{
				VarsJson: varsJSON, ID: runID,
			}); err != nil {
				return nil, fmt.Errorf("mark superseded coordinator ask: %w", err)
			}
		}
		supersededRows, err = s.planCancellationBoundaries(ctx, tx, replaced, "", "canceled", exitReasonSupersededByWorkflowStart)
		if err != nil {
			return nil, err
		}
		for i := range replaced {
			op, planned := mutation.ReplacementTeardowns[replaced[i].ID]
			if err := s.insertTreeTeardownTx(ctx, tx, &replaced[i], op, planned, exitReasonSupersededByWorkflowStart); err != nil {
				return nil, err
			}
			canceled := replaced[i]
			canceled.Status = api.WorkflowRunStatusCanceled
			canceled.PauseReason = exitReasonSupersededByWorkflowStart
			canceled.CompletedAt = &now
			canceled.UpdatedAt = now
			canceled.Revision++
			if err := s.enqueueRunTx(ctx, tx, &canceled, api.WorkflowEventKindRunCanceled, ""); err != nil {
				return nil, err
			}
		}
	}
	if err := s.createState(ctx, queries, replacement, mutation.ProjectDir, mutation.Vars); err != nil {
		return nil, err
	}
	if err := s.enqueueRunTx(ctx, tx, replacement, api.WorkflowEventKindRunStarted, ""); err != nil {
		return nil, err
	}
	// Superseded boundaries keep their place ahead of the new run's own rows.
	if err := s.appendPlannedRowsTx(ctx, tx, replacement.SessionID, supersededRows); err != nil {
		return nil, err
	}
	if err := s.appendPlannedRowsTx(ctx, tx, replacement.SessionID, mutation.Messages); err != nil {
		return nil, err
	}
	if mutation.Posture != "" {
		if s.sessions == nil {
			return nil, fmt.Errorf("workflow posture transaction participant unavailable")
		}
		if err := s.sessions.SetPostureTx(ctx, tx, replacement.SessionID, mutation.Posture); err != nil {
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
	if s.outbox != nil {
		s.outbox.Notify()
	}
	return replaced, nil
}

// ReplayCommand returns an exact committed retry.
func (s *SQLStore) ReplayCommand(ctx context.Context, runID string, sourceRevision int64, kind, inputDigest string) (*api.WorkflowRun, bool, error) {
	stored, err := s.queries.GetWorkflowCommandByRevision(ctx, db.GetWorkflowCommandByRevisionParams{
		RunID: runID, SourceRevision: sourceRevision,
	})
	if db.IsNoRows(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if stored.Kind != kind || stored.InputDigest != inputDigest {
		return nil, false, fmt.Errorf("%w: run %s revision %d was consumed by another command", ErrRunRevisionConflict, runID, sourceRevision)
	}
	return decodeWorkflowCommandReceipt(stored.Kind, stored.InputDigest, stored.ResponseJson, stored.RejectionJson, kind, inputDigest)
}

func (s *SQLStore) ReplayCommandOperation(ctx context.Context, operationID, kind, inputDigest string) (*api.WorkflowRun, bool, error) {
	stored, err := s.queries.GetWorkflowCommandByOperation(ctx, operationID)
	if db.IsNoRows(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if stored.Kind != kind || stored.InputDigest != inputDigest {
		return nil, false, fmt.Errorf("%w: workflow operation %s was used for another command", ErrRunRevisionConflict, operationID)
	}
	return decodeWorkflowCommandReceipt(stored.Kind, stored.InputDigest, stored.ResponseJson, stored.RejectionJson, kind, inputDigest)
}

func decodeWorkflowCommandReceipt(storedKind, storedDigest, response, rejectionJSON, kind, inputDigest string) (*api.WorkflowRun, bool, error) {
	if storedKind != kind || storedDigest != inputDigest {
		return nil, false, ErrRunRevisionConflict
	}
	var run api.WorkflowRun
	if err := json.Unmarshal([]byte(response), &run); err != nil {
		return nil, false, fmt.Errorf("decode workflow command receipt: %w", err)
	}
	if rejectionJSON != "" {
		var rejection workflowCommandRejection
		if err := json.Unmarshal([]byte(rejectionJSON), &rejection); err != nil {
			return nil, false, fmt.Errorf("decode workflow command rejection: %w", err)
		}
		if rejection.Kind != "phase_gate_unmet" {
			return nil, false, fmt.Errorf("unknown workflow command rejection %q", rejection.Kind)
		}
		return &run, true, &PhaseGateUnmetError{
			Phase: rejection.Phase, Reason: rejection.Reason, FailedGate: rejection.FailedGate,
			FailedLeaves: append([]string(nil), rejection.Leaves...), Replayed: true,
		}
	}
	return &run, true, nil
}

// CommitCommand commits one workflow mutation.
func (s *SQLStore) CommitCommand(ctx context.Context, run *api.WorkflowRun, mutation workflowCommandMutation) error {
	if run == nil || mutation.OperationID == "" || mutation.Kind == "" || mutation.InputDigest == "" {
		return fmt.Errorf("complete workflow command required")
	}
	sourceRevision := run.Revision
	committed := false
	defer func() {
		if !committed {
			run.Revision = sourceRevision
		}
	}()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	queries := db.New(tx)
	previous, err := queries.GetWorkflowRun(ctx, run.ID)
	if err != nil {
		return err
	}
	if run.UpdatedAt.IsZero() {
		run.UpdatedAt = time.Now().UTC()
	}
	failureJSON, err := db.MarshalJSON(run.Failure)
	if err != nil {
		return fmt.Errorf("encode workflow failure: %w", err)
	}
	var changed int64
	if mutation.Vars != nil {
		raw, marshalErr := json.Marshal(mutation.Vars)
		if marshalErr != nil {
			return marshalErr
		}
		changed, err = queries.CommitWorkflowRunState(ctx, db.CommitWorkflowRunStateParams{
			Status: string(run.Status), CurrentPhase: run.CurrentPhase, ProjectDir: mutation.ProjectDir,
			VarsJson: string(raw), PauseReason: db.NullString(run.PauseReason), FailureJson: failureJSON, UpdatedAt: db.FormatTime(run.UpdatedAt),
			PausedAt: db.NullTimePtr(run.PausedAt), CompletedAt: db.NullTimePtr(run.CompletedAt), ID: run.ID, Revision: sourceRevision,
		})
	} else {
		changed, err = queries.UpdateWorkflowRun(ctx, db.UpdateWorkflowRunParams{
			Status: string(run.Status), CurrentPhase: run.CurrentPhase, PauseReason: db.NullString(run.PauseReason),
			FailureJson: failureJSON, UpdatedAt: db.FormatTime(run.UpdatedAt), PausedAt: db.NullTimePtr(run.PausedAt),
			CompletedAt: db.NullTimePtr(run.CompletedAt), ID: run.ID, Revision: sourceRevision,
		})
	}
	if err != nil {
		return err
	}
	if changed == 0 {
		_ = tx.Rollback()
		replayed, ok, replayErr := s.ReplayCommand(ctx, run.ID, sourceRevision, mutation.Kind, mutation.InputDigest)
		if replayErr != nil {
			return replayErr
		}
		if ok {
			*run = *replayed
			return nil
		}
		return s.revisionConflict(ctx, run.ID, sourceRevision)
	}
	run.Revision++
	event, previousPhase := workflowMutationEvent(previous.CurrentPhase, run.CurrentPhase)
	if err := s.enqueueRunTx(ctx, tx, run, event, previousPhase); err != nil {
		return err
	}
	if err := s.appendPlannedRowsTx(ctx, tx, run.SessionID, mutation.Messages); err != nil {
		return err
	}
	if strings.TrimSpace(run.EndMessageID) != "" {
		if err := queries.SetWorkflowRunEndMessageID(ctx, db.SetWorkflowRunEndMessageIDParams{
			EndMessageID: db.NullString(run.EndMessageID), ID: run.ID,
		}); err != nil {
			return err
		}
	}
	if mutation.Posture != "" {
		if s.sessions == nil {
			return fmt.Errorf("workflow posture transaction participant unavailable")
		}
		if err := s.sessions.SetPostureTx(ctx, tx, run.SessionID, mutation.Posture); err != nil {
			return err
		}
	}
	if mutation.Workers.CancelAll && mutation.Workers.CancelRunning {
		return fmt.Errorf("workflow worker mutation cannot cancel all and running-only")
	}
	if mutation.Workers.HoldPending {
		err = queries.HoldWorkflowWorkers(ctx, db.NullString(run.ID))
	}
	if err == nil && mutation.Workers.CancelRunning {
		err = queries.RequestRunningWorkflowWorkerCancellation(ctx, db.RequestRunningWorkflowWorkerCancellationParams{
			RequestedAt: db.NullString(db.FormatTime(time.Now().UTC())), WorkflowRunID: db.NullString(run.ID),
		})
	}
	if err == nil && mutation.Workers.CancelAll {
		err = queries.RequestWorkflowWorkerCancellation(ctx, db.RequestWorkflowWorkerCancellationParams{
			RequestedAt: db.NullString(db.FormatTime(time.Now().UTC())), WorkflowRunID: db.NullString(run.ID),
		})
	}
	if err == nil && mutation.Workers.ReleaseHeld {
		err = queries.ReleaseWorkflowWorkers(ctx, db.NullString(run.ID))
	}
	if err != nil {
		return err
	}
	if mutation.Workers != (workflowWorkerMutation{}) {
		// Publish worker events in the workflow transaction.
		if err := worker.EnqueueRunJobEventsTx(ctx, tx, s.outbox, run.ID); err != nil {
			return err
		}
	}
	if err := insertTeardownTx(ctx, tx, mutation.Teardown); err != nil {
		return err
	}
	response, err := json.Marshal(run)
	if err != nil {
		return err
	}
	rejectionJSON := ""
	if mutation.Rejection != nil {
		rejection, marshalErr := json.Marshal(workflowCommandRejection{
			Kind: "phase_gate_unmet", Phase: mutation.Rejection.Phase, Reason: mutation.Rejection.Reason,
			FailedGate: mutation.Rejection.FailedGate, Leaves: append([]string(nil), mutation.Rejection.FailedLeaves...),
		})
		if marshalErr != nil {
			return marshalErr
		}
		rejectionJSON = string(rejection)
	}
	if err := queries.InsertWorkflowCommand(ctx, db.InsertWorkflowCommandParams{
		OperationID: mutation.OperationID, RunID: run.ID, SourceRevision: sourceRevision,
		Kind: mutation.Kind, InputDigest: mutation.InputDigest, ResultRevision: run.Revision,
		ResponseJson: string(response), RejectionJson: rejectionJSON, CommittedAt: db.FormatTime(time.Now().UTC()),
	}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	if s.outbox != nil {
		s.outbox.Notify()
	}
	if mutation.Workers.ReleaseHeld && s.workers != nil {
		s.workers.NotifyRunnable()
	}
	return nil
}

// CommitBlueprintApproval commits reviewed bytes and the workflow gate.
// The approver is the deciding person; channel records how the approval arrived.
func (s *SQLStore) CommitBlueprintApproval(ctx context.Context, run *api.WorkflowRun, projectDir string, vars map[string]any, digest, channel string) error {
	// The session identifies the grant throughout its lifetime.
	if run == nil || strings.TrimSpace(run.ProjectID) == "" || strings.TrimSpace(run.BlueprintPath) == "" ||
		strings.TrimSpace(digest) == "" || strings.TrimSpace(run.SessionID) == "" {
		return fmt.Errorf("canonical blueprint approval is incomplete")
	}
	// Validate the seal before issuing the grant.
	if s.authz == nil {
		return authzledger.ErrSealFailed
	}
	approver, err := people.Deciding(ctx)
	if err != nil {
		return fmt.Errorf("blueprint approver: %w", err)
	}
	raw, err := json.Marshal(vars)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UTC()
	queries := db.New(tx)
	n, err := queries.UpdateWorkflowRunVars(ctx, db.UpdateWorkflowRunVarsParams{
		ProjectDir: projectDir, VarsJson: string(raw), UpdatedAt: db.FormatTime(now), ID: run.ID, Revision: run.Revision,
	})
	if err != nil {
		return err
	}
	if n == 0 {
		_ = tx.Rollback()
		return s.revisionConflict(ctx, run.ID, run.Revision)
	}
	if err := queries.UpsertBlueprintApproval(ctx, db.UpsertBlueprintApprovalParams{
		ProjectID: run.ProjectID, Path: run.BlueprintPath, ContentDigest: digest,
		WorkflowRunID: db.NullString(run.ID), WorkflowRevision: run.Revision,
		ApprovedAt: db.FormatTime(now), ApprovedVia: strings.TrimSpace(channel), ApprovedByPersonID: approver.ID,
		SessionID: strings.TrimSpace(run.SessionID),
	}); err != nil {
		return err
	}
	if err := s.authz.AppendHumanGateTx(ctx, tx, authzledger.HumanGateRecord{
		SessionID:        run.SessionID,
		Action:           authzledger.ActionBlueprintApproved,
		Outcome:          authzledger.OutcomeAllowed,
		ResolvedBy:       authzledger.ResolvedByHuman,
		ResolverPersonID: approver.ID,
		Files:            []string{run.BlueprintPath},
		ProjectDir:       projectDir,
		BlueprintDigest:  digest,
	}); err != nil {
		return err
	}
	next := *run
	next.Revision++
	next.UpdatedAt = now
	if err := s.enqueueRunTx(ctx, tx, &next, api.WorkflowEventKindRunUpdated, ""); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	*run = next
	s.outbox.Notify()
	return nil
}

func (s *SQLStore) BlueprintApprovalMatches(ctx context.Context, projectID, path, runID string, revision int64, digest string) (bool, error) {
	count, err := s.queries.CountMatchingBlueprintApproval(ctx, db.CountMatchingBlueprintApprovalParams{
		ProjectID: projectID, Path: path, WorkflowRunID: db.NullString(runID),
		WorkflowRevision: revision, ContentDigest: digest,
	})
	return count == 1, err
}

// CreateState inserts a fully initialized workflow run.
func (s *SQLStore) CreateState(ctx context.Context, run *api.WorkflowRun, projectDir string, vars map[string]any) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.createState(ctx, db.New(tx), run, projectDir, vars); err != nil {
		return err
	}
	if err := s.enqueueRunTx(ctx, tx, run, api.WorkflowEventKindRunStarted, ""); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.outbox.Notify()
	return nil
}

func (s *SQLStore) createState(ctx context.Context, queries *db.Queries, run *api.WorkflowRun, projectDir string, vars map[string]any) error {
	if run == nil {
		return fmt.Errorf("workflow run required")
	}
	if run.ID == "" {
		run.ID = uuid.NewString()
	}
	now := time.Now().UTC()
	if run.CreatedAt.IsZero() {
		run.CreatedAt = now
	}
	if run.UpdatedAt.IsZero() {
		run.UpdatedAt = now
	}
	if run.Revision <= 0 {
		run.Revision = 1
	}
	if strings.TrimSpace(run.ProjectID) == "" {
		projectID, err := queries.GetSessionProjectID(ctx, run.SessionID)
		if err != nil {
			return fmt.Errorf("resolve workflow project: %w", err)
		}
		run.ProjectID = projectID
	}
	if vars == nil {
		vars = map[string]any{}
	}
	varsJSON, err := json.Marshal(vars)
	if err != nil {
		return fmt.Errorf("encode workflow vars: %w", err)
	}
	failureJSON, err := db.MarshalJSON(run.Failure)
	if err != nil {
		return fmt.Errorf("encode workflow failure: %w", err)
	}
	return queries.InsertWorkflowRun(ctx, db.InsertWorkflowRunRowParams{
		ID:              run.ID,
		SessionID:       run.SessionID,
		ProjectID:       run.ProjectID,
		WorkflowID:      run.WorkflowID,
		WorkflowVersion: run.WorkflowVersion,
		AttachPolicy:    run.AttachPolicy,
		Status:          string(run.Status),
		ParentRunID:     db.NullString(parentRunID(run.ParentRunID)),
		Revision:        run.Revision,
		CurrentPhase:    run.CurrentPhase,
		ProjectDir:      projectDir,
		VarsJson:        string(varsJSON),
		BlueprintPath:   db.NullString(run.BlueprintPath),
		PauseReason:     db.NullString(run.PauseReason),
		FailureJson:     failureJSON,
		StartMessageID:  db.NullString(run.StartMessageID),
		EndMessageID:    db.NullString(run.EndMessageID),
		CreatedAt:       db.FormatTime(run.CreatedAt),
		UpdatedAt:       db.FormatTime(run.UpdatedAt),
		PausedAt:        db.NullTimePtr(run.PausedAt),
		CompletedAt:     db.NullTimePtr(run.CompletedAt),
	})
}

// StartChild atomically pauses the parent and inserts the initialized child.
func (s *SQLStore) StartChild(ctx context.Context, parent, child *api.WorkflowRun, mutation workflowChildStartMutation) error {
	if parent == nil || child == nil {
		return fmt.Errorf("parent and child workflow runs required")
	}
	if parent.SessionID == "" || child.SessionID != parent.SessionID || child.ParentRunID == nil || strings.TrimSpace(*child.ParentRunID) != parent.ID {
		return fmt.Errorf("child must belong to and reference parent workflow run")
	}
	tx, err := s.db.BeginTx(ctx, nil)
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
		return s.revisionConflict(ctx, parent.ID, parent.Revision)
	}
	if err := s.createState(ctx, queries, child, mutation.ProjectDir, mutation.Vars); err != nil {
		return err
	}
	parentNext := *parent
	parentNext.Revision++
	if err := s.enqueueRunTx(ctx, tx, &parentNext, api.WorkflowEventKindRunUpdated, ""); err != nil {
		return err
	}
	if err := s.enqueueRunTx(ctx, tx, child, api.WorkflowEventKindRunStarted, ""); err != nil {
		return err
	}
	if err := s.appendPlannedRowsTx(ctx, tx, parent.SessionID, mutation.Messages); err != nil {
		return err
	}
	if mutation.Posture != "" {
		if s.sessions == nil {
			return fmt.Errorf("workflow posture transaction participant unavailable")
		}
		if err := s.sessions.SetPostureTx(ctx, tx, parent.SessionID, mutation.Posture); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	*parent = parentNext
	if s.outbox != nil {
		s.outbox.Notify()
	}
	return nil
}

// CancelActiveTree terminates one active lineage.
func (s *SQLStore) CancelActiveTree(ctx context.Context, target *api.WorkflowRun, reason string, posture api.SessionPosture, teardowns map[string]*workflowTeardownIntent) ([]api.WorkflowRun, error) {
	if target == nil {
		return nil, fmt.Errorf("target workflow run required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	runs, err := s.listBySession(ctx, db.New(tx), target.SessionID, 64, []string{
		string(api.WorkflowRunStatusRunning),
		string(api.WorkflowRunStatusPaused),
		string(api.WorkflowRunStatusPausedOnChild),
	})
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	queries := db.New(tx)
	changed, err := queries.CancelActiveRootWorkflowLineage(ctx, db.CancelActiveRootWorkflowLineageParams{
		Reason: db.NullString(reason), CompletedAt: db.NullString(db.FormatTime(now)), UpdatedAt: db.FormatTime(now),
		TargetSessionID: target.SessionID, ExpectedID: target.ID, ExpectedRevision: target.Revision,
	})
	if err != nil {
		return nil, err
	}
	if changed == 0 {
		_ = tx.Rollback()
		return nil, s.revisionConflict(ctx, target.ID, target.Revision)
	}
	boundaries, err := s.planCancellationBoundaries(ctx, tx, runs, target.ID, "exited", reason)
	if err != nil {
		return nil, err
	}
	for i := range runs {
		op, planned := teardowns[runs[i].ID]
		if err := s.insertTreeTeardownTx(ctx, tx, &runs[i], op, planned, reason); err != nil {
			return nil, err
		}
		canceled := runs[i]
		canceled.Status = api.WorkflowRunStatusCanceled
		canceled.PauseReason = reason
		canceled.CompletedAt = &now
		canceled.UpdatedAt = now
		canceled.Revision++
		if err := s.enqueueRunTx(ctx, tx, &canceled, api.WorkflowEventKindRunCanceled, ""); err != nil {
			return nil, err
		}
	}
	if err := s.appendPlannedRowsTx(ctx, tx, target.SessionID, boundaries); err != nil {
		return nil, err
	}
	if posture != "" {
		if s.sessions == nil {
			return nil, fmt.Errorf("workflow posture transaction participant unavailable")
		}
		if err := s.sessions.SetPostureTx(ctx, tx, target.SessionID, posture); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	if s.outbox != nil {
		s.outbox.Notify()
	}
	return runs, nil
}

// planCancellationBoundaries reserves row IDs before run events are queued.
func (s *SQLStore) planCancellationBoundaries(
	ctx context.Context,
	tx *sql.Tx,
	runs []api.WorkflowRun,
	targetID, targetEvent, reason string,
) ([]api.Message, error) {
	if len(runs) == 0 {
		return nil, nil
	}
	if s.sessions == nil {
		return nil, fmt.Errorf("workflow transcript transaction participant unavailable")
	}
	queries := db.New(tx)
	messages := make([]api.Message, 0, len(runs))
	for i := range runs {
		event := "canceled"
		if runs[i].ID == targetID && targetEvent != "" {
			event = targetEvent
		}
		message := newCommandBoundary(&runs[i], runs[i].Revision, event, runs[i].CurrentPhase, reason)
		runs[i].EndMessageID = message.ID
		if err := queries.SetWorkflowRunEndMessageID(ctx, db.SetWorkflowRunEndMessageIDParams{
			EndMessageID: db.NullString(message.ID), ID: runs[i].ID,
		}); err != nil {
			return nil, err
		}
		messages = append(messages, message)
	}
	return messages, nil
}

// appendPlannedRowsTx writes rows after their run events are queued.
func (s *SQLStore) appendPlannedRowsTx(
	ctx context.Context,
	tx *sql.Tx,
	sessionID string,
	msgs []api.Message,
) error {
	if len(msgs) == 0 {
		return nil
	}
	if s.sessions == nil {
		return fmt.Errorf("workflow transcript transaction participant unavailable")
	}
	return s.sessions.AppendMessagesTx(ctx, tx, sessionID, msgs...)
}

// Get returns a workflow run by id.
func (s *SQLStore) Get(ctx context.Context, id string) (*api.WorkflowRun, error) {
	row, err := s.queries.GetWorkflowRun(ctx, id)
	if db.IsNoRows(err) {
		return nil, ErrRunNotFound
	}
	if err != nil {
		return nil, err
	}
	return runFromRow(row)
}

// Update commits mutable run fields at the current revision.
func (s *SQLStore) Update(ctx context.Context, run *api.WorkflowRun) error {
	if run == nil {
		return fmt.Errorf("workflow run required")
	}
	if run.UpdatedAt.IsZero() {
		run.UpdatedAt = time.Now().UTC()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	queries := db.New(tx)
	previous, err := queries.GetWorkflowRun(ctx, run.ID)
	if err != nil {
		return err
	}
	failureJSON, err := db.MarshalJSON(run.Failure)
	if err != nil {
		return fmt.Errorf("encode workflow failure: %w", err)
	}
	n, err := queries.UpdateWorkflowRun(ctx, db.UpdateWorkflowRunParams{
		Status:       string(run.Status),
		CurrentPhase: run.CurrentPhase,
		PauseReason:  db.NullString(run.PauseReason),
		FailureJson:  failureJSON,
		UpdatedAt:    db.FormatTime(run.UpdatedAt),
		PausedAt:     db.NullTimePtr(run.PausedAt),
		CompletedAt:  db.NullTimePtr(run.CompletedAt),
		ID:           run.ID,
		Revision:     run.Revision,
	})
	if err != nil {
		return err
	}
	if n == 0 {
		_ = tx.Rollback()
		return s.revisionConflict(ctx, run.ID, run.Revision)
	}
	next := *run
	next.Revision++
	event, previousPhase := workflowMutationEvent(previous.CurrentPhase, next.CurrentPhase)
	if err := s.enqueueRunTx(ctx, tx, &next, event, previousPhase); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	*run = next
	s.outbox.Notify()
	return nil
}

// CommitState commits run fields and variables together.
func (s *SQLStore) CommitState(ctx context.Context, run *api.WorkflowRun, projectDir string, vars map[string]any) error {
	if run == nil {
		return fmt.Errorf("workflow run required")
	}
	if vars == nil {
		vars = map[string]any{}
	}
	raw, err := json.Marshal(vars)
	if err != nil {
		return err
	}
	if run.UpdatedAt.IsZero() {
		run.UpdatedAt = time.Now().UTC()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	queries := db.New(tx)
	previous, err := queries.GetWorkflowRun(ctx, run.ID)
	if err != nil {
		return err
	}
	failureJSON, err := db.MarshalJSON(run.Failure)
	if err != nil {
		return fmt.Errorf("encode workflow failure: %w", err)
	}
	n, err := queries.CommitWorkflowRunState(ctx, db.CommitWorkflowRunStateParams{
		Status:       string(run.Status),
		CurrentPhase: run.CurrentPhase,
		ProjectDir:   projectDir,
		VarsJson:     string(raw),
		PauseReason:  db.NullString(run.PauseReason),
		FailureJson:  failureJSON,
		UpdatedAt:    db.FormatTime(run.UpdatedAt),
		PausedAt:     db.NullTimePtr(run.PausedAt),
		CompletedAt:  db.NullTimePtr(run.CompletedAt),
		ID:           run.ID,
		Revision:     run.Revision,
	})
	if err != nil {
		return err
	}
	if n == 0 {
		_ = tx.Rollback()
		return s.revisionConflict(ctx, run.ID, run.Revision)
	}
	next := *run
	next.Revision++
	event, previousPhase := workflowMutationEvent(previous.CurrentPhase, next.CurrentPhase)
	if err := s.enqueueRunTx(ctx, tx, &next, event, previousPhase); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	*run = next
	s.outbox.Notify()
	return nil
}

func workflowMutationEvent(previousPhase, currentPhase string) (event api.WorkflowEventKind, previous string) {
	if previousPhase != currentPhase {
		return api.WorkflowEventKindPhaseAdvanced, previousPhase
	}
	return api.WorkflowEventKindRunUpdated, ""
}

// ActiveBySession returns the active leaf run (running child, or running/paused root).
func (s *SQLStore) ActiveBySession(ctx context.Context, sessionID string) (*api.WorkflowRun, error) {
	row, err := s.queries.ActiveWorkflowRunBySession(ctx, sessionID)
	if db.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return runFromRow(row)
}

// ActiveStateBySession returns an active run and its variables from one row.
func (s *SQLStore) ActiveStateBySession(ctx context.Context, sessionID string) (*api.WorkflowRun, map[string]any, error) {
	row, err := s.queries.ActiveWorkflowRunBySession(ctx, sessionID)
	if db.IsNoRows(err) {
		return nil, map[string]any{}, nil
	}
	if err != nil {
		return nil, nil, err
	}
	run, err := runFromRow(row)
	if err != nil {
		return nil, nil, err
	}
	vars, err := scaffoldVarsFromJSON(row.VarsJson)
	if err != nil {
		return nil, nil, err
	}
	return run, vars, nil
}

// UpdateVars commits variables at the current revision.
func (s *SQLStore) UpdateVars(ctx context.Context, run *api.WorkflowRun, projectDir string, vars map[string]any) error {
	if run == nil {
		return fmt.Errorf("workflow run required")
	}
	if vars == nil {
		vars = map[string]any{}
	}
	b, err := json.Marshal(vars)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UTC()
	n, err := db.New(tx).UpdateWorkflowRunVars(ctx, db.UpdateWorkflowRunVarsParams{
		ProjectDir: projectDir,
		VarsJson:   string(b),
		UpdatedAt:  db.FormatTime(now),
		ID:         run.ID,
		Revision:   run.Revision,
	})
	if err != nil {
		return err
	}
	if n == 0 {
		_ = tx.Rollback()
		return s.revisionConflict(ctx, run.ID, run.Revision)
	}
	next := *run
	next.Revision++
	next.UpdatedAt = now
	if err := s.enqueueRunTx(ctx, tx, &next, api.WorkflowEventKindRunUpdated, ""); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	*run = next
	s.outbox.Notify()
	return nil
}

func (s *SQLStore) revisionConflict(ctx context.Context, runID string, expected int64) error {
	row, err := s.queries.GetWorkflowRun(ctx, runID)
	if err != nil {
		return fmt.Errorf("%w: run %s expected revision %d", ErrRunRevisionConflict, runID, expected)
	}
	return fmt.Errorf("%w: run %s expected revision %d, actual %d", ErrRunRevisionConflict, runID, expected, row.Revision)
}

// GetScaffoldVars returns the variables stored with a workflow run.
func (s *SQLStore) GetScaffoldVars(ctx context.Context, workflowRunID string) (map[string]any, error) {
	raw, err := s.queries.GetScaffoldVars(ctx, workflowRunID)
	if db.IsNoRows(err) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	return scaffoldVarsFromJSON(raw)
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

// ListBySession returns workflow runs for a session, newest first.
func (s *SQLStore) ListBySession(ctx context.Context, sessionID string, limit int, statusFilter []string) ([]api.WorkflowRun, error) {
	return s.listBySession(ctx, s.queries, sessionID, limit, statusFilter)
}

// ListRunning returns every run whose host obligation may need recovery.
func (s *SQLStore) ListRunning(ctx context.Context) ([]api.WorkflowRun, error) {
	rows, err := s.queries.ListRunningWorkflowRuns(ctx)
	if err != nil {
		return nil, err
	}
	runs := make([]api.WorkflowRun, 0, len(rows))
	for _, row := range rows {
		run, mapErr := runFromRow(row)
		if mapErr != nil {
			return nil, mapErr
		}
		runs = append(runs, *run)
	}
	return runs, nil
}

// ListPausedOnChild returns every parent whose child-exit transition may need recovery.
func (s *SQLStore) ListPausedOnChild(ctx context.Context) ([]api.WorkflowRun, error) {
	rows, err := s.queries.ListPausedOnChildWorkflowRuns(ctx)
	if err != nil {
		return nil, err
	}
	runs := make([]api.WorkflowRun, 0, len(rows))
	for _, row := range rows {
		run, mapErr := runFromRow(row)
		if mapErr != nil {
			return nil, mapErr
		}
		runs = append(runs, *run)
	}
	return runs, nil
}

func (s *SQLStore) listBySession(ctx context.Context, queries *db.Queries, sessionID string, limit int, statusFilter []string) ([]api.WorkflowRun, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	statuses := make([]string, 0, len(statusFilter))
	for _, st := range statusFilter {
		statuses = append(statuses, strings.TrimSpace(st))
	}
	var rows []db.WorkflowRuns
	var err error
	if len(statuses) > 0 {
		rows, err = queries.ListWorkflowRunsBySessionWithStatus(ctx, db.ListWorkflowRunsBySessionWithStatusParams{
			SessionID: sessionID,
			Statuses:  statuses,
			Limit:     int64(limit),
		})
	} else {
		rows, err = queries.ListWorkflowRunsBySession(ctx, db.ListWorkflowRunsBySessionParams{
			SessionID: sessionID,
			Limit:     int64(limit),
		})
	}
	if err != nil {
		return nil, err
	}
	out := make([]api.WorkflowRun, 0, len(rows))
	for _, r := range rows {
		run, err := runFromRow(r)
		if err != nil {
			return nil, err
		}
		out = append(out, *run)
	}
	return out, nil
}

func (s *SQLStore) RelocateBlueprintPath(ctx context.Context, projectID, from, to string) ([]string, error) {
	projectID = strings.TrimSpace(projectID)
	from = strings.TrimSpace(from)
	to = strings.TrimSpace(to)
	if projectID == "" || from == "" || to == "" || from == to {
		return nil, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := s.queries.WithTx(tx).RelocateWorkflowRunBlueprintPaths(ctx, db.RelocateWorkflowRunBlueprintPathsParams{
		ToPath:    db.NullString(to),
		UpdatedAt: db.FormatTime(time.Now().UTC()),
		ProjectID: projectID,
		FromPath:  db.NullString(from),
	})
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(rows))
	out := make([]string, 0, len(rows))
	for _, sessionID := range rows {
		sessionID = strings.TrimSpace(sessionID)
		if sessionID == "" {
			continue
		}
		if _, ok := seen[sessionID]; ok {
			continue
		}
		seen[sessionID] = struct{}{}
		out = append(out, sessionID)
	}
	return out, nil
}

// ActiveByProjectForBlueprint returns a non-terminal run bound to a project blueprint path.
func (s *SQLStore) ActiveByProjectForBlueprint(ctx context.Context, projectID, blueprintPath string) (*api.WorkflowRun, error) {
	projectID = strings.TrimSpace(projectID)
	blueprintPath = strings.TrimSpace(blueprintPath)
	if projectID == "" || blueprintPath == "" {
		return nil, nil
	}
	row, err := s.queries.ActiveWorkflowRunByProjectAndBlueprintPath(ctx, db.ActiveWorkflowRunByProjectAndBlueprintPathParams{
		ProjectID:     projectID,
		BlueprintPath: db.NullString(blueprintPath),
	})
	if db.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return runFromRow(row)
}

// LatestChildByParentRunID returns the most recent child run for a parent (any status).
func (s *SQLStore) LatestChildByParentRunID(ctx context.Context, parentRunID string) (*api.WorkflowRun, error) {
	parentRunID = strings.TrimSpace(parentRunID)
	if parentRunID == "" {
		return nil, nil
	}
	row, err := s.queries.LatestChildWorkflowRun(ctx, db.NullString(parentRunID))
	if db.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return runFromRow(row)
}

func parentRunID(id *string) string {
	if id == nil {
		return ""
	}
	return strings.TrimSpace(*id)
}

// runFromRow maps a generated workflow_runs row onto the wire type.
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

var _ RunStore = (*SQLStore)(nil)
