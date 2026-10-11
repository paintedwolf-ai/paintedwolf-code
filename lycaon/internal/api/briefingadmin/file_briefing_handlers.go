package briefingadmin

import (
	"errors"
	"net/http"
	"strings"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/filebriefing"
	"github.com/lycaon/lycaon/internal/project"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Handler) HandleRequestFileBriefing(w http.ResponseWriter, r *http.Request) {
	p, ok := requestscope.ProjectByURLID(s.ProjectRegistry, s.responses, w, r)
	if !ok {
		return
	}
	p, ok = requestscope.ProjectForRequest(s.SessionStore, s.responses, w, r, p)
	if !ok {
		return
	}
	if !s.FileBriefings.Enabled() {
		s.responses.Fail(w, wire.ApiErrorCodeFileBriefingDisabled, "File summaries are off")
		return
	}
	var req wire.FileBriefingRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	req.RootID, req.Path = strings.TrimSpace(req.RootID), strings.TrimSpace(req.Path)
	req.Presentation, req.Trigger = strings.TrimSpace(req.Presentation), strings.TrimSpace(req.Trigger)
	req.WorkerID, req.DocumentID = strings.TrimSpace(req.WorkerID), strings.TrimSpace(req.DocumentID)
	req.VersionID = strings.TrimSpace(req.VersionID)
	if req.RootID == "" || req.Path == "" {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "root_id and path are required")
		return
	}
	if req.Trigger != "automatic" && req.Trigger != "manual" {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "trigger must be automatic or manual")
		return
	}
	resolved, err := s.resolveFileBriefing(r.Context(), p, req)
	if err != nil {
		if code, message, ok := FileBriefingRequestError(err); ok {
			s.responses.Fail(w, code, message)
		} else {
			s.responses.InternalError(w, r, err)
		}
		return
	}
	briefing, err := s.FileBriefings.Request(r.Context(), resolved.target(p), req.Trigger)
	if err != nil {
		s.writeFileBriefingError(w, r, err)
		return
	}
	status := http.StatusAccepted
	if briefing.Status != filebriefing.StatusPending {
		status = http.StatusOK
	}
	httpio.WriteJSON(w, status, filebriefing.Response(briefing))
}

func (s *Handler) HandleGetFileBriefing(w http.ResponseWriter, r *http.Request) {
	p, ok := requestscope.ProjectByURLID(s.ProjectRegistry, s.responses, w, r)
	if !ok {
		return
	}
	p, ok = requestscope.ProjectForRequest(s.SessionStore, s.responses, w, r, p)
	if !ok {
		return
	}
	if !s.FileBriefings.Enabled() {
		s.responses.Fail(w, wire.ApiErrorCodeFileBriefingDisabled, "File summaries are off")
		return
	}
	rootID, path := strings.TrimSpace(r.URL.Query().Get("root_id")), strings.TrimSpace(r.URL.Query().Get("path"))
	if rootID == "" {
		s.responses.InvalidQueryParam(w, "root_id", "is required")
		return
	}
	if path == "" {
		s.responses.InvalidQueryParam(w, "path", "is required")
		return
	}
	documentRevision, _, queryErr := httpio.OptionalIntQuery(r, "document_revision", 1, 0)
	if queryErr != nil {
		s.responses.InvalidQuery(w, queryErr)
		return
	}
	req := wire.FileBriefingRequest{
		RootID: rootID, Path: path, Presentation: strings.TrimSpace(r.URL.Query().Get("presentation")), Trigger: "automatic",
		WorkerID: strings.TrimSpace(r.URL.Query().Get("worker_id")), DocumentID: strings.TrimSpace(r.URL.Query().Get("document_id")),
		DocumentRevision: documentRevision, VersionID: strings.TrimSpace(r.URL.Query().Get("version_id")),
	}
	resolved, err := s.resolveFileBriefing(r.Context(), p, req)
	if err != nil {
		if code, message, ok := FileBriefingRequestError(err); ok {
			s.responses.Fail(w, code, message)
		} else {
			s.responses.InternalError(w, r, err)
		}
		return
	}
	briefing, err := s.FileBriefings.Get(r.Context(), resolved.target(p))
	if err != nil {
		s.writeFileBriefingError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, filebriefing.Response(briefing))
}

func projectRootPath(p *project.Project, rootID string) string {
	if p != nil {
		for _, root := range p.Roots {
			if root.ID == rootID {
				return root.Path
			}
		}
	}
	return ""
}

func (s *Handler) writeFileBriefingError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, filebriefing.ErrDisabled):
		s.responses.Fail(w, wire.ApiErrorCodeFileBriefingDisabled, "File summaries are off")
	case errors.Is(err, filebriefing.ErrStopped):
		s.responses.Unavailable(w, wire.ApiErrorCodeFileBriefingUnavailable, "file briefings are stopping")
	case errors.Is(err, filebriefing.ErrNotFound):
		s.responses.Fail(w, wire.ApiErrorCodeFileBriefingNotFound, "no file briefing has been requested")
	default:
		s.responses.InternalError(w, r, err)
	}
}
