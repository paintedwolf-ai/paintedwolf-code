package briefingadmin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/briefing"
	"github.com/lycaon/lycaon/internal/briefinghost"
	"github.com/lycaon/lycaon/internal/briefingstore"
	"github.com/lycaon/lycaon/internal/filebriefing"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// Services are the briefing kinds the routes serve: file summaries and the
// shared engine's profiles.
type Services struct {
	Files *filebriefing.Service
	Host  *briefinghost.Host
}

// EngineRoutes serves the shared engine's profiles and evidence records.
type EngineRoutes struct {
	services  Services
	projects  project.Registry
	sessions  session.Store
	responses *httpio.Responder
}

// Host is the shared engine, or nil where only file summaries are served.
func (s *EngineRoutes) Host() *briefinghost.Host { return s.services.Host }

// busyRetrySeconds is how long a person waits before asking a full queue again.
const busyRetrySeconds = 5

func (s *EngineRoutes) HandleRequestBriefing(w http.ResponseWriter, r *http.Request) {
	p, ok := s.briefingProject(w, r)
	if !ok {
		return
	}
	var req wire.BriefingRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	if req.Trigger == "" {
		req.Trigger = "automatic"
	}
	if req.Trigger != "automatic" && req.Trigger != "manual" {
		s.responses.InvalidField(w, "trigger", "must be automatic or manual")
		return
	}
	profile, err := s.services.Host.Registry.Profile(req.ProfileID)
	if err != nil {
		s.responses.Fail(w, wire.ApiErrorCodeBriefingTargetUnavailable, "This kind of briefing is not available")
		return
	}
	ref, err := json.Marshal(req.TargetRef)
	if err != nil {
		s.responses.InvalidField(w, "target_ref", "must be an object")
		return
	}
	request, err := profile.Resolve(r.Context(), briefing.Target{ProjectID: p.ID, ProjectDir: projectDir(p), Ref: ref, Manual: req.Trigger == "manual"})
	if err == nil {
		_, err = s.services.Host.Engine.Request(r.Context(), request)
	}
	if err != nil {
		s.writeBriefingError(w, r, err)
		return
	}
	// Provenance is best effort: a failed link leaves the briefing as it is.
	if err := s.services.Host.Store.LinkApprovedRationale(r.Context(), request); err != nil {
		s.responses.Logger.WarnContext(r.Context(), "link approved rationale", "profile", request.ProfileID, "error", err)
	}
	snapshot, err := s.services.Host.Store.Snapshot(r.Context(), briefingstore.SnapshotQuery{
		ProjectID: p.ID, ProfileID: profile.ID(), Version: profile.Version(), Policy: profile.Policy(), TargetKey: request.TargetKey,
	})
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, snapshot)
}

func (s *EngineRoutes) HandleGetBriefingRecord(w http.ResponseWriter, r *http.Request) {
	p, ok := requestscope.ProjectByURLID(s.projects, s.responses, w, r)
	if !ok {
		return
	}
	readable := func(ctx context.Context, audience string) (bool, error) {
		if audience == "" {
			return true, nil
		}
		_, err := requestscope.ResolveSessionProject(s.sessions, ctx, p, audience)
		if errors.Is(err, store.ErrSessionNotFound) {
			return false, nil
		}
		return err == nil, err
	}
	record, err := s.services.Host.Store.Record(r.Context(), p.ID, strings.TrimSpace(chi.URLParam(r, "record_id")), readable)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		s.responses.Fail(w, wire.ApiErrorCodeBriefingRecordNotFound, "No evidence record has that id")
	case err != nil:
		s.responses.InternalError(w, r, err)
	default:
		httpio.WriteJSON(w, http.StatusOK, record)
	}
}

func (s *EngineRoutes) briefingProject(w http.ResponseWriter, r *http.Request) (*project.Project, bool) {
	p, ok := requestscope.ProjectByURLID(s.projects, s.responses, w, r)
	if !ok {
		return nil, false
	}
	if !s.services.Files.Enabled() {
		s.responses.Fail(w, wire.ApiErrorCodeBriefingDisabled, "Summaries are off")
		return nil, false
	}
	return p, true
}

func projectDir(p *project.Project) string {
	if len(p.Roots) == 0 {
		return ""
	}
	return p.Roots[0].Path
}

func (s *EngineRoutes) writeBriefingError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, briefing.ErrInvalidTarget):
		s.responses.InvalidField(w, "target_ref", err.Error())
	case errors.Is(err, briefing.ErrTargetUnavailable):
		s.responses.Fail(w, wire.ApiErrorCodeBriefingTargetUnavailable, "This target cannot be explained now")
	case errors.Is(err, store.ErrSessionNotFound):
		s.responses.Fail(w, wire.ApiErrorCodeSessionNotFound, "Session not found")
	case errors.Is(err, briefing.ErrBusy):
		w.Header().Set("Retry-After", strconv.Itoa(busyRetrySeconds))
		s.responses.Fail(w, wire.ApiErrorCodeBriefingBusy, "Too many briefings are waiting")
	case errors.Is(err, briefing.ErrStopped):
		s.responses.Unavailable(w, wire.ApiErrorCodeBriefingUnavailable, "Briefings are stopping")
	default:
		s.responses.InternalError(w, r, err)
	}
}
