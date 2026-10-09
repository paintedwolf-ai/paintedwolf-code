package workflowadmin

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/project"
	workflowcatalog "github.com/lycaon/lycaon/internal/workflow/catalog"
	workflowpersistence "github.com/lycaon/lycaon/internal/workflow/persistence"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *RunControl) HandleListWorkflows(w http.ResponseWriter, r *http.Request) {
	// Workflow discovery accepts no project directory.
	projectDir, ok := s.optionalProjectDirFromQuery(w, r)
	if !ok {
		return
	}
	projectID := strings.TrimSpace(r.URL.Query().Get("project_id"))
	sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
	if sessionID != "" {
		sess, ok := requestscope.Session(s.Store, s.responses, w, r, sessionID)
		if !ok {
			return
		}
		if projectDir == "" && strings.TrimSpace(sess.WorkspacePath) != "" {
			projectDir = sess.WorkspacePath
		}
		if projectID == "" {
			projectID = strings.TrimSpace(sess.ProjectID)
		}
	}
	catalog := s.Catalog
	if projectID != "" {
		catalog = workflowcatalog.Resolver{
			SessionStore: s.Catalog.SessionStore,
			CatalogFor:   s.Sessions.Catalog.CatalogForProjectDir(projectID),
			// Project workflows retain their trust gate.
			ProjectTierApplies: s.Catalog.ProjectTierApplies,
		}
	}
	summaries, excluded, err := catalog.ListResolvedWithExcluded(r.Context(), projectDir, sessionID)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	if summaries == nil {
		summaries = []wire.WorkflowSummary{}
	}
	excludedByPath := make(map[string]wire.ExcludedWorkflow, len(excluded))
	for _, entry := range excluded {
		if previous, ok := excludedByPath[entry.Path]; ok {
			entry.Errors = append(previous.Errors, entry.Errors...)
		}
		excludedByPath[entry.Path] = entry
	}
	resp := wire.WorkflowListResponse{
		Workflows: summaries,
		Excluded:  excludedByPath,
	}
	httpio.WriteJSON(w, http.StatusOK, resp)
}

var workflowRunPageLimit = httpio.MustPageLimit(20, 1, 100)

func (s *RunControl) HandleListSessionWorkflowRuns(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "id")
	if !requestscope.SessionExists(s.Store, s.responses, w, r, sessionID) {
		return
	}
	pq, err := httpio.ReadPageQuery(r, workflowRunPageLimit)
	if err != nil {
		s.responses.InvalidQuery(w, err)
		return
	}
	var statusFilter []string
	if raw := strings.TrimSpace(r.URL.Query().Get("status")); raw != "" {
		validStatuses := make(map[string]struct{}, len(wire.AllWorkflowRunStatuses()))
		for _, status := range wire.AllWorkflowRunStatuses() {
			validStatuses[string(status)] = struct{}{}
		}
		for _, st := range strings.Split(raw, ",") {
			st = strings.TrimSpace(st)
			if st != "" {
				if _, ok := validStatuses[st]; !ok {
					s.responses.InvalidQueryParam(w, "status", "contains an unknown workflow run status")
					return
				}
				statusFilter = append(statusFilter, st)
			}
		}
	}
	page, err := s.Runs.Runs.ListPageBySession(r.Context(), sessionID, pq.Limit, statusFilter, pq.Cursor)
	if err != nil {
		if errors.Is(err, workflowpersistence.ErrInvalidRunPageCursor) {
			s.responses.PageCursorError(w, r, "cursor", err)
			return
		}
		s.responses.InternalError(w, r, err)
		return
	}
	if page.Runs == nil {
		page.Runs = []wire.WorkflowRun{}
	}
	runPtrs := make([]*wire.WorkflowRun, len(page.Runs))
	for i := range page.Runs {
		runPtrs[i] = &page.Runs[i]
	}
	s.SessionView.EnrichWorkflowRuns(r.Context(), runPtrs)
	httpio.WriteJSON(w, http.StatusOK, page)
}

func (s *RunControl) optionalProjectDirFromQuery(w http.ResponseWriter, r *http.Request) (projectDir string, ok bool) {
	projectID := strings.TrimSpace(r.URL.Query().Get("project_id"))
	if projectID == "" {
		return "", true
	}
	p, err := s.Projects.Get(r.Context(), projectID)
	if err != nil {
		if errors.Is(err, project.ErrNotFound) {
			s.responses.Fail(w, wire.ApiErrorCodeProjectNotFound, "project not found")
			return "", false
		}
		s.responses.InternalError(w, r, err)
		return "", false
	}
	return project.PrimaryRootPath(p), true
}
