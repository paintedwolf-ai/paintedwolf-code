package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/board"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/worker"
	wire "github.com/lycaon/lycaon/pkg/api"
)

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

func (s *Workers) handleWorkerCancel(w http.ResponseWriter, r *http.Request) {
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

func (s *Workers) handleGetBoard(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(chi.URLParam(r, "id"))
	if projectID == "" {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "project id is required")
		return
	}
	p, ok := requestscope.ProjectByID(s.projectRegistry, s.responses, w, r, projectID)
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
