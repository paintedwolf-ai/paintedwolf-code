package projectadmin

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/projectview"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectcontrib"
	"github.com/lycaon/lycaon/internal/settings"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Trust) HandleGetProjectTrust(w http.ResponseWriter, r *http.Request) {
	p, ok := s.trustProject(w, r)
	if !ok {
		return
	}
	s.writeProjectTrust(w, r, p)
}

func (s *Trust) HandleUpdateProjectTrust(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(chi.URLParam(r, "id"))
	var raw map[string]any
	var req wire.UpdateProjectTrustRequest
	if err := httpio.DecodeJSONWithRaw(w, r, &req, &raw); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	if !s.responses.RequireJSONObjectKeys(w, raw, "enabled") {
		return
	}
	p, ok := s.trustProject(w, r)
	if !ok {
		return
	}
	if len(req.Enabled) > 0 && !s.validTrustSwitches(w, req.Enabled) {
		return
	}
	scan, err := projectcontrib.ProcessInventory().Scan(r.Context(), project.RootPaths(p))
	if err != nil {
		s.WriteProjectInventoryError(w, r, p.ID, err)
		return
	}
	if len(req.Enabled) > 0 {
		updated, err := s.Registry.SetTrustEnabled(r.Context(), id, req.Enabled)
		if err != nil {
			s.responses.ProjectRegistryError(w, r, err)
			return
		}
		updated.TrustSeen = p.TrustSeen
		p = updated
		s.Extensions.InvalidateEffectiveCatalog(r.Context(), id)
	}
	if len(req.Enabled) > 0 {
		projectview.PublishEvent(s.Registry, s.Events, r.Context(), wire.ProjectEventUpdated, p)
		projectview.PublishSettings(s.Events, s.Registry, r.Context(), wire.SettingsAreaProjectTrust, "project", p.ID, "updated")
	}

	httpio.WriteJSON(w, http.StatusOK, settings.ProjectTrustToDTO(s.Settings.TrustSurfaces, *p, scan))
}

func (s *Trust) trustProject(w http.ResponseWriter, r *http.Request) (*project.Project, bool) {
	id := strings.TrimSpace(chi.URLParam(r, "id"))
	p, err := s.Registry.Get(r.Context(), id)
	if err != nil {
		s.responses.ProjectLookupError(w, r, err)
		return nil, false
	}
	p.TrustSeen, err = s.Registry.ReadTrustBaseline(r.Context(), id)
	if err != nil {
		s.responses.ProjectLookupError(w, r, err)
		return nil, false
	}
	return p, true
}

// Trust status reuses recent inventory; review and save scan fresh.
const trustStatusMaxAge = 5 * time.Second

func (s *Trust) writeProjectTrust(w http.ResponseWriter, r *http.Request, p *project.Project) {
	scan, err := projectcontrib.ProcessInventory().ScanRecent(r.Context(), project.RootPaths(p), trustStatusMaxAge)
	if err != nil {
		s.WriteProjectInventoryError(w, r, p.ID, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, settings.ProjectTrustToDTO(s.Settings.TrustSurfaces, *p, scan))
}

func (s *Trust) HandleGetTrustSettings(w http.ResponseWriter, r *http.Request) {
	httpio.WriteJSON(w, http.StatusOK, settings.TrustSettingsToDTO(s.Settings.TrustSurfaces))
}

func (s *Trust) HandleUpdateTrustSettings(w http.ResponseWriter, r *http.Request) {
	var raw map[string]any
	var req wire.UpdateTrustSettingsRequest
	if err := httpio.DecodeJSONWithRaw(w, r, &req, &raw); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	if !s.responses.RequireJSONObjectKeys(w, raw, "enabled") {
		return
	}
	if !s.validTrustSwitches(w, req.Enabled) {
		return
	}
	if err := s.Settings.TrustSurfaces.PutEnabled(req.Enabled); err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	s.Extensions.InvalidateEffectiveCatalog(r.Context(), "")
	projectview.PublishSettings(s.Events, s.Registry, r.Context(), wire.SettingsAreaProjectTrust, "global", "", "updated")
	httpio.WriteJSON(w, http.StatusOK, settings.TrustSettingsToDTO(s.Settings.TrustSurfaces))
}

// Opening Trust acknowledges the shared comparison without replacing it.
func (s *Trust) HandleOpenProjectTrustReview(w http.ResponseWriter, r *http.Request) {
	p, ok := s.trustProject(w, r)
	if !ok {
		return
	}
	scan, err := projectcontrib.ProcessInventory().Scan(r.Context(), project.RootPaths(p))
	if err != nil {
		s.WriteProjectInventoryError(w, r, p.ID, err)
		return
	}
	records := settings.SeenRecordsFor(*p, scan)
	current := settings.ProjectTrustToDTO(s.Settings.TrustSurfaces, *p, scan)
	if current.UnreadCount == 0 {
		httpio.WriteJSON(w, http.StatusOK, current)
		return
	}
	settings.RetainTrustComparison(records, p.TrustSeen)
	opened := time.Now().UTC()
	settings.StampTrustReadTime(records, opened)
	updated, err := s.Registry.MarkTrustSeen(r.Context(), p.ID, p, records)
	if errors.Is(err, project.ErrTrustReviewChanged) {
		s.responses.Fail(w, wire.ApiErrorCodeTrustReviewChanged, "project roots changed while Trust was open")
		return
	}
	if err != nil {
		s.responses.ProjectRegistryError(w, r, err)
		return
	}
	projectview.PublishEvent(s.Registry, s.Events, r.Context(), wire.ProjectEventUpdated, updated)
	projectview.PublishSettings(s.Events, s.Registry, r.Context(), wire.SettingsAreaProjectTrust, "project", p.ID, "updated")
	updated.TrustSeen = records
	httpio.WriteJSON(w, http.StatusOK, settings.ProjectTrustToDTO(s.Settings.TrustSurfaces, *updated, scan))
}

func (s *Trust) WriteProjectInventoryError(w http.ResponseWriter, r *http.Request, projectID string, err error) {
	if r.Context().Err() != nil {
		s.responses.InternalError(w, r, r.Context().Err())
		return
	}
	if s.responses.Logger != nil {
		s.responses.Logger.WarnContext(r.Context(), "project contribution inventory unavailable", "project_id", projectID, "err", err)
	}
	s.responses.Fail(w, wire.ApiErrorCodeProjectInventoryUnavailable, "project contributions could not be read")
}

// validTrustSwitches rejects trust surface ids a person cannot switch.
func (s *Trust) validTrustSwitches(w http.ResponseWriter, enabled map[string]bool) bool {
	if err := settings.ValidateTrustSwitchIds(enabled); err != nil {
		s.responses.FailDetails(w, wire.ApiErrorCodeInvalidRequest,
			map[string]any{"field": "enabled", "reason": "names a trust surface that cannot be switched"},
			"enabled names a trust surface that cannot be switched")
		return false
	}
	return true
}
