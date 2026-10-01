package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/cost"
	"github.com/lycaon/lycaon/internal/pagecursor"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Server) handleCostSummary(w http.ResponseWriter, r *http.Request) {
	scope, sessionID, projectID, ok := s.parseCostSummaryQuery(w, r)
	if !ok {
		return
	}
	summary, err := s.costTracker.Summary(r.Context(), scope, sessionID, projectID)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	if projectID != "" {
		summary.ProjectID = projectID
	}
	httpio.WriteJSON(w, http.StatusOK, summary)
}

var projectCostReportLimit = httpio.MustPageLimit(40, 1, 200)

func (s *Server) handleProjectCostReport(w http.ResponseWriter, r *http.Request) {
	p, ok := requestscope.ProjectByURLID(s.projectRegistry, &s.responses, w, r)
	if !ok {
		return
	}
	page, err := httpio.ReadPageQuery(r, projectCostReportLimit)
	if err != nil {
		s.responses.InvalidQuery(w, err)
		return
	}
	report, err := s.costTracker.ProjectReport(r.Context(), p.ID, cost.ReportQuery{
		Limit: page.Limit, Cursor: page.Cursor, Search: r.URL.Query().Get("q"), Sort: r.URL.Query().Get("sort"),
	})
	if errors.Is(err, pagecursor.ErrInvalid) || errors.Is(err, pagecursor.ErrExpired) {
		s.responses.PageCursorError(w, r, "cursor", err)
		return
	}
	if errors.Is(err, cost.ErrInvalidReportQuery) {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidQuery, "sort must be cost, tokens, activity, or id, and q at most 500 characters")
		return
	}
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, report)
}

// parseCostSummaryQuery selects one cost scope.
func (s *Server) parseCostSummaryQuery(w http.ResponseWriter, r *http.Request) (scope wire.CostScope, sessionID, projectID string, ok bool) {
	sessionID = strings.TrimSpace(r.URL.Query().Get("session_id"))
	projectID = strings.TrimSpace(r.URL.Query().Get("project_id"))

	switch {
	case sessionID != "" && projectID != "":
		s.responses.InvalidQueryParam(w, "project_id", "must not be combined with session_id")
		return "", "", "", false
	case sessionID != "":
		if !requestscope.SessionExists(s.sessionStore, &s.responses, w, r, sessionID) {
			return "", "", "", false
		}
		return wire.CostScopeSession, sessionID, "", true
	case projectID != "":
		resolvedID, _, ok := requestscope.ProjectIDQuery(s.projectRegistry, &s.responses, w, r)
		if !ok {
			return "", "", "", false
		}
		return wire.CostScopeProject, "", resolvedID, true
	default:
		s.responses.InvalidQueryParam(w, "session_id", "session_id or project_id is required")
		return "", "", "", false
	}
}
