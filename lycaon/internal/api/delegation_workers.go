package api

import (
	"errors"
	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/board"
	"github.com/lycaon/lycaon/internal/delegation"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session/lifecycle"
	"github.com/lycaon/lycaon/internal/worker"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Server) handleCreateDelegation(w http.ResponseWriter, r *http.Request) {
	var req wire.CreateDelegationRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	req.ProjectID = strings.TrimSpace(req.ProjectID)
	req.Task = strings.TrimSpace(req.Task)
	req.OperationID = strings.TrimSpace(req.OperationID)
	if req.OperationID == "" || req.ProjectID == "" || req.Task == "" {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "operation_id, project_id and task are required")
		return
	}
	if _, err := uuid.Parse(req.OperationID); err != nil {
		s.responses.FailDetails(w, wire.ApiErrorCodeInvalidRequest, map[string]any{"field": "operation_id"}, "operation_id must be a valid UUID")
		return
	}
	if _, ok := requestscope.ProjectByID(s.projectRegistry, &s.responses, w, r, req.ProjectID); !ok {
		return
	}
	if req.Strategy != "" && !validHuntStrategy(req.Strategy) {
		s.responses.FailDetails(w, wire.ApiErrorCodeInvalidRequest, map[string]any{"field": "strategy"}, "strategy must be one of file-based, feature-based, risk-based, research-based")
		return
	}
	if req.InspectMode != "" && !validInspectMode(req.InspectMode) {
		s.responses.FailDetails(w, wire.ApiErrorCodeInvalidRequest, map[string]any{"field": "inspect_mode"}, "inspect_mode must be one of standard, turbo, full")
		return
	}
	out, err := s.delegations.Create(r.Context(), req)
	if errors.Is(err, delegation.ErrOperationConflict) {
		s.responses.Fail(w, wire.ApiErrorCodeIdempotencyConflict, "operation_id was already used with a different request")
		return
	}
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusCreated, out)
}

func (s *Server) handleGetDelegation(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	out, err := s.delegations.GetStatus(r.Context(), id)
	if err != nil {
		if errors.Is(err, delegation.ErrDelegationNotFound) {
			s.responses.Fail(w, wire.ApiErrorCodeDelegationNotFound, "delegation not found")
			return
		}
		s.responses.InternalError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, out)
}

func (s *Server) handleListDelegationLegs(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := s.delegations.GetStatus(r.Context(), id); err != nil {
		if errors.Is(err, delegation.ErrDelegationNotFound) {
			s.responses.Fail(w, wire.ApiErrorCodeDelegationNotFound, "delegation not found")
			return
		}
		s.responses.InternalError(w, r, err)
		return
	}
	legs, err := s.delegations.Store.ListLegs(r.Context(), id)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	if legs == nil {
		legs = []wire.Leg{}
	}
	httpio.WriteJSON(w, http.StatusOK, wire.DelegationLegListResponse{Legs: legs})
}

func (s *Server) handleDispatchDelegationLeg(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := s.delegations.GetStatus(r.Context(), id); err != nil {
		if errors.Is(err, delegation.ErrDelegationNotFound) {
			s.responses.Fail(w, wire.ApiErrorCodeDelegationNotFound, "delegation not found")
			return
		}
		s.responses.InternalError(w, r, err)
		return
	}
	var req wire.DispatchRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	legID := strings.TrimSpace(req.LegID)
	if legID == "" {
		legs, err := s.delegations.Store.ListLegs(r.Context(), id)
		if err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
		for _, leg := range legs {
			if leg.Status == wire.LegStatusPending {
				legID = leg.ID
				break
			}
		}
		if legID == "" {
			s.responses.Fail(w, wire.ApiErrorCodeLegNotPending, "no pending leg to dispatch")
			return
		}
	} else {
		leg, err := s.delegations.Store.GetLeg(r.Context(), id, legID)
		if errors.Is(err, delegation.ErrLegNotFound) || (err == nil && leg == nil) {
			s.responses.Fail(w, wire.ApiErrorCodeDelegationLegNotFound, "delegation leg not found")
			return
		}
		if err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
	}
	leg, err := s.delegations.DispatchLeg(r.Context(), id, legID, "")
	if err != nil {
		if errors.Is(err, lifecycle.ErrStopping) {
			s.responses.Fail(w, wire.ApiErrorCodeSessionStopping, "the coordinator session is stopping")
			return
		}
		if errors.Is(err, delegation.ErrLegNotPending) {
			s.responses.Fail(w, wire.ApiErrorCodeLegNotPending, "the delegation leg is not pending")
			return
		}
		if errors.Is(err, delegation.ErrLegNotFound) {
			s.responses.Fail(w, wire.ApiErrorCodeDelegationLegNotFound, "delegation leg not found")
			return
		}
		if errors.Is(err, delegation.ErrDelegationNotFound) {
			s.responses.Fail(w, wire.ApiErrorCodeDelegationNotFound, "delegation not found")
			return
		}
		s.responses.InternalError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, leg)
}

func (s *Server) handleAbortDelegation(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req wire.AbortDelegationRequest
	if _, err := httpio.DecodeOptionalJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	if err := s.delegations.Abort(r.Context(), id, req.Reason); err != nil {
		if errors.Is(err, delegation.ErrDelegationNotFound) {
			s.responses.Fail(w, wire.ApiErrorCodeDelegationNotFound, "delegation not found")
			return
		}
		s.responses.InternalError(w, r, err)
		return
	}
	out, err := s.delegations.GetStatus(r.Context(), id)
	if err != nil {
		if errors.Is(err, delegation.ErrDelegationNotFound) {
			s.responses.Fail(w, wire.ApiErrorCodeDelegationNotFound, "delegation not found")
			return
		}
		s.responses.InternalError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, out)
}

func validWorkerStatus(status wire.WorkerStatus) bool {
	switch status {
	case wire.WorkerStatusPending,
		wire.WorkerStatusRunning,
		wire.WorkerStatusWaiting,
		wire.WorkerStatusComplete,
		wire.WorkerStatusFailed,
		wire.WorkerStatusCanceled,
		wire.WorkerStatusHeld:
		return true
	default:
		return false
	}
}

func (s *Server) handleWorkerCancel(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := s.workerCancel.CancelJob(r.Context(), id, ""); err != nil {
		var reject *toolrejection.ToolReject
		if errors.As(err, &reject) && reject.Code == worker.WorkerCancelNotFoundCode {
			s.responses.Fail(w, wire.ApiErrorCodeWorkerNotFound, "worker not found")
			return
		}
		if errors.As(err, &reject) && reject.Code == worker.WorkerCancelTerminalCode {
			s.responses.Fail(w, wire.ApiErrorCodeWorkerNotCancelable, "the worker can no longer be canceled")
			return
		}
		s.responses.InternalError(w, r, err)
		return
	}
	// Graceful cancellation stays running until the worker publishes its terminal event.
	task, ok := s.workers.Get(id)
	if !ok {
		s.responses.Fail(w, wire.ApiErrorCodeWorkerNotFound, "worker not found")
		return
	}
	httpio.WriteJSON(w, http.StatusAccepted, task)
}

func (s *Server) handleGetBoard(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(chi.URLParam(r, "id"))
	if projectID == "" {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "project id is required")
		return
	}
	p, ok := requestscope.ProjectByID(s.projectRegistry, &s.responses, w, r, projectID)
	if !ok {
		return
	}
	sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
	if sessionID == "" {
		s.responses.FailDetails(w, wire.ApiErrorCodeInvalidQuery, map[string]any{"param": "session_id"}, "session_id query parameter is required")
		return
	}
	scope, err := requestscope.ResolveSessionProject(s.sessionStore, r.Context(), p, sessionID)
	if err != nil {
		s.Git.WriteGitReposLoadError(w, r, err)
		return
	}
	projectPath := project.PrimaryRootPath(scope.Project)
	if scope.Binding != nil {
		projectPath = scope.Binding.WorktreePath
	}
	levelStr := strings.TrimSpace(r.URL.Query().Get("detail_level"))
	level := wire.BoardDetailLevelCompact
	if levelStr != "" {
		level = wire.BoardDetailLevel(levelStr)
		if !validBoardDetailLevel(level) {
			s.responses.FailDetails(w, wire.ApiErrorCodeInvalidQuery, map[string]any{"param": "detail_level"}, "invalid detail_level query parameter")
			return
		}
	}
	snap, err := s.board.Build(r.Context(), projectID, projectPath, sessionID, level, scope.Roots)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, board.BuildBoardView(snap, sessionID, level, time.Now().UTC()))
}

func validBoardDetailLevel(level wire.BoardDetailLevel) bool {
	switch level {
	case wire.BoardDetailLevelStatus,
		wire.BoardDetailLevelCompact,
		wire.BoardDetailLevelFull,
		wire.BoardDetailLevelForensic:
		return true
	default:
		return false
	}
}
