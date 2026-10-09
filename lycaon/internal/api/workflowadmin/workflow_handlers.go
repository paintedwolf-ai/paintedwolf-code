package workflowadmin

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/hostctx"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Handler) HandleStartWorkflowRun(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "id")
	var req wire.StartWorkflowRunRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	operationID, err := uuid.Parse(strings.TrimSpace(req.OperationID))
	if err != nil {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "operation_id must be a UUID")
		return
	}
	req.OperationID = operationID.String()
	req.WorkflowID = strings.TrimSpace(req.WorkflowID)
	req.WorkflowVersion = strings.TrimSpace(req.WorkflowVersion)
	req.ReplaceRunID = strings.TrimSpace(req.ReplaceRunID)
	if (req.ReplaceRunID == "") != (req.ExpectedRevision == 0) || req.ExpectedRevision < 0 {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidWorkflowReplacementTarget, "replace_run_id and expected_revision must be supplied together")
		return
	}
	if req.WorkflowID == "" || req.WorkflowVersion == "" {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "workflow_id and workflow_version are required")
		return
	}
	sess, ok := requestscope.Session(s.Store, s.responses, w, r, sessionID)
	if !ok {
		return
	}
	if err := s.Workflows.Resolver.ValidateUserFacingStart(r.Context(), sess.WorkspacePath, sessionID, req.WorkflowID, req.WorkflowVersion); err != nil {
		s.WriteWorkflowError(w, r, err)
		return
	}
	run, err := s.Workflows.Starts.Start(hostctx.WithHumanWorkflowStart(r.Context()), sessionID, req)
	if err != nil {
		s.WriteWorkflowError(w, r, err)
		return
	}
	s.StartOrchestratedTopologyForRun(r.Context(), sessionID, run)
	s.SessionView.WriteWorkflowRun(w, r, http.StatusCreated, run)
}

func (s *Handler) HandleExitWorkflowRun(w http.ResponseWriter, r *http.Request) {
	runID := strings.TrimSpace(chi.URLParam(r, "id"))
	if runID == "" {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "id is required")
		return
	}
	var req wire.WorkflowControlRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	if req.ExpectedRevision < 1 {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidWorkflowTarget, "expected_revision is required and must be positive")
		return
	}
	run, err := s.Workflows.Store.Runs.Get(r.Context(), runID)
	if err != nil {
		s.WriteWorkflowError(w, r, err)
		return
	}
	exitedRun, err := s.Workflows.Controls.Exit(r.Context(), run.SessionID, runID, req.ExpectedRevision, strings.TrimSpace(req.Reason))
	if err != nil {
		s.WriteWorkflowError(w, r, err)
		return
	}
	s.SessionView.WriteWorkflowRun(w, r, http.StatusOK, exitedRun)
}

func (s *Handler) HandleGetActiveWorkflowRun(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "id")
	if !requestscope.SessionExists(s.Store, s.responses, w, r, sessionID) {
		return
	}
	run, err := s.Workflows.Store.Runs.ActiveBySession(r.Context(), sessionID)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	s.SessionView.EnrichWorkflowRun(r.Context(), run)
	httpio.WriteJSON(w, http.StatusOK, wire.ActiveWorkflowRunResponse{Run: run})
}

func (s *Handler) HandleGetWorkflowRun(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "id")
	run, err := s.Workflows.Store.Runs.Get(r.Context(), runID)
	if err != nil {
		s.writeRunLookupError(w, r, err)
		return
	}
	s.SessionView.WriteWorkflowRun(w, r, http.StatusOK, run)
}

func (s *Handler) HandlePauseWorkflowRun(w http.ResponseWriter, r *http.Request) {
	s.controlWorkflowRun(w, r, func(ctx context.Context, runID string, reason string) (*wire.WorkflowRun, error) {
		return s.Workflows.Controls.Pause(ctx, runID, reason)
	})
}

func (s *Handler) HandleResumeWorkflowRun(w http.ResponseWriter, r *http.Request) {
	s.controlWorkflowRun(w, r, func(ctx context.Context, runID string, _ string) (*wire.WorkflowRun, error) {
		return s.Workflows.Controls.Resume(ctx, runID)
	})
}

func (s *Handler) HandleCancelWorkflowRun(w http.ResponseWriter, r *http.Request) {
	s.controlWorkflowRun(w, r, func(ctx context.Context, runID string, reason string) (*wire.WorkflowRun, error) {
		return s.Workflows.Controls.Cancel(ctx, runID, reason)
	})
}

func (s *Handler) HandleAdvanceWorkflowRun(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "id")
	var req wire.AdvanceWorkflowRunRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	if req.ExpectedRevision < 1 {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidWorkflowRevision, "expected_revision must be positive")
		return
	}
	run, err := s.Workflows.Phases.Advance(runstate.WithExpectedRevision(r.Context(), req.ExpectedRevision), runID)
	if err != nil {
		var gateErr *runstate.PhaseGateUnmetError
		if errors.As(err, &gateErr) && !gateErr.Replayed {
			if active, gerr := s.Workflows.Store.Runs.Get(r.Context(), runID); gerr == nil && active != nil {
				// A committed gate rejection emits one coordinator nudge.
				s.Sessions.Guidance.Emit(r.Context(), active.SessionID, anchor.GateBlocked, anchor.Envelope{})
				s.Sessions.Coordinator.Runtime.CoordinatorLoop().Nudge(
					r.Context(),
					active.SessionID,
					anchor.PhaseAdvanced,
					anchor.GateBlocked,
					"",
					anchor.Envelope{},
				)
			}
		}
		s.WriteWorkflowError(w, r, err)
		return
	}
	s.SessionView.WriteWorkflowRun(w, r, http.StatusOK, run)
}

func (s *Handler) HandleFireWorkflowTransition(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "id")
	transitionID := chi.URLParam(r, "transition_id")
	var req wire.FireWorkflowTransitionRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	if req.ExpectedRevision < 1 {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidWorkflowRevision, "expected_revision must be positive")
		return
	}
	run, err := s.Workflows.Phases.FireTransition(runstate.WithExpectedRevision(r.Context(), req.ExpectedRevision), runID, transitionID, workflowdef.TransitionActorHuman)
	if err != nil {
		s.WriteWorkflowError(w, r, err)
		return
	}
	s.SessionView.WriteWorkflowRun(w, r, http.StatusOK, run)
}

type workflowControlFn func(ctx context.Context, runID, reason string) (*wire.WorkflowRun, error)

func (s *Handler) controlWorkflowRun(w http.ResponseWriter, r *http.Request, fn workflowControlFn) {
	runID := chi.URLParam(r, "id")
	var req wire.WorkflowControlRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	if req.ExpectedRevision < 1 {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidWorkflowRevision, "expected_revision must be positive")
		return
	}
	run, err := fn(runstate.WithExpectedRevision(r.Context(), req.ExpectedRevision), runID, strings.TrimSpace(req.Reason))
	if err != nil {
		s.WriteWorkflowError(w, r, err)
		return
	}
	s.SessionView.WriteWorkflowRun(w, r, http.StatusOK, run)
}
