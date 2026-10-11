package api

import (
	"errors"
	"net/http"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/pagecursor"
	"github.com/lycaon/lycaon/internal/worker"
	wire "github.com/lycaon/lycaon/pkg/api"
)

var workerListLimit = httpio.MustPageLimit(100, 1, worker.MaxSessionPageSize)

func (s *Workers) handleListWorkers(w http.ResponseWriter, r *http.Request) {
	projectID, _, ok := requestscope.ProjectIDQuery(s.projectRegistry, s.responses, w, r)
	if !ok {
		return
	}
	sessionID, present, err := httpio.SingleQueryValue(r, "session_id")
	if err != nil {
		s.responses.InvalidQuery(w, err)
		return
	}
	if !present {
		s.responses.InvalidQueryParam(w, "session_id", "is required")
		return
	}
	status, _, err := httpio.SingleQueryValue(r, "status")
	if err != nil {
		s.responses.InvalidQuery(w, err)
		return
	}
	if status != "" && !validWorkerStatus(wire.WorkerStatus(status)) {
		s.responses.InvalidQueryParam(w, "status", "must name a worker status")
		return
	}
	query, err := httpio.ReadPageQuery(r, workerListLimit)
	if err != nil {
		s.responses.InvalidQuery(w, err)
		return
	}
	page, err := s.workers.ListSessionPage(r.Context(), worker.SessionPageQuery{ProjectID: projectID, SessionID: sessionID, Status: wire.WorkerStatus(status), Cursor: query.Cursor, Limit: query.Limit})
	if errors.Is(err, pagecursor.ErrInvalid) || errors.Is(err, pagecursor.ErrExpired) {
		s.responses.PageCursorError(w, r, "cursor", err)
		return
	}
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, wire.WorkerListResponse{Workers: page.Workers, NextCursor: page.NextCursor})
}
