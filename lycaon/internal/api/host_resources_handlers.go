package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/projectview"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/api/settingsadmin"
	"github.com/lycaon/lycaon/internal/hostresources"
	"github.com/lycaon/lycaon/internal/projectcontrib"
	"github.com/lycaon/lycaon/internal/settings"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Server) handleGetHostResources(w http.ResponseWriter, r *http.Request) {
	snap, err := s.hostResourceSnapshot(r, false)
	if err != nil {
		requestscope.ScopeError(&s.responses, w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, hostResourcesWire(snap))
}

func (s *Server) handleRefreshHostResources(w http.ResponseWriter, r *http.Request) {
	snap, err := s.hostResourceSnapshot(r, true)
	if err != nil {
		requestscope.ScopeError(&s.responses, w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, hostResourcesWire(snap))
}

func (s *Server) handleUpdateHostResource(w http.ResponseWriter, r *http.Request) {
	scope, ref, err := requestscope.Settings(r, s.projectRegistry)
	projectDir := ref.Dir
	if err != nil {
		requestscope.ScopeError(&s.responses, w, r, err)
		return
	}
	id := strings.TrimSpace(chi.URLParam(r, "resource_id"))
	known := false
	for _, state := range s.hostResources.Snapshot(r.Context(), false).Resources {
		if state.ID == id {
			known = true
			break
		}
	}
	if !known {
		s.responses.Fail(w, wire.ApiErrorCodeHostResourceNotFound, "unknown host resource")
		return
	}
	var req wire.UpdateHostResourceRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	var effect settings.ApprovalEffect
	switch req.Access {
	case wire.HostResourceAccessSettingInherit:
	case wire.HostResourceAccessSettingAsk:
		effect = settings.ApprovalEffectAsk
	case wire.HostResourceAccessSettingDeny:
		effect = settings.ApprovalEffectDeny
	default:
		s.responses.FailDetails(w, wire.ApiErrorCodeInvalidRequest, map[string]any{"field": "access"}, "access must be inherit, ask, or deny")
		return
	}
	if err := s.settingsSvc.Approvals.SetHostResourceRule(scope, projectDir, id, effect); err != nil {
		if !settingsadmin.ProjectApprovalsWriteError(&s.responses, w, err) {
			s.responses.InternalError(w, r, err)
		}
		return
	}
	projectview.PublishSettings(s.events, s.projectRegistry, r.Context(), wire.SettingsAreaApprovals, string(scope), projectDir, "updated")
	project := hostresources.ProjectContext{ID: ref.ID, Dir: projectDir}
	httpio.WriteJSON(w, http.StatusOK, hostResourcesWire(s.hostResources.SnapshotFor(r.Context(), false, project, nil)))
}

func (s *Server) hostResourceSnapshot(r *http.Request, refresh bool) (hostresources.Snapshot, error) {
	projectID := strings.TrimSpace(r.URL.Query().Get("project_id"))
	if projectID != "" {
		p, err := s.projectRegistry.Get(r.Context(), projectID)
		if err != nil {
			return hostresources.Snapshot{}, err
		}
		dir, err := requestscope.ProjectDir(r, s.projectRegistry)
		if err != nil {
			return hostresources.Snapshot{}, err
		}
		projectDir := requestscope.GatedProjectDir(s.settingsSvc, p, dir, projectcontrib.SurfaceScanConfig)
		project := hostresources.ProjectContext{ID: projectID, Dir: projectDir}
		return s.hostResources.SnapshotFor(r.Context(), refresh, project, nil), nil
	}
	return s.hostResources.Snapshot(r.Context(), refresh), nil
}

func hostResourcesWire(snapshot hostresources.Snapshot) wire.HostResourcesResponse {
	items := make([]wire.HostResource, 0, len(snapshot.Resources))
	for _, state := range snapshot.Resources {
		connections := make([]wire.HostResourceConnection, 0, len(state.Connections))
		for _, connection := range state.Connections {
			connections = append(connections, wire.HostResourceConnection{
				Mode:      wire.HostResourceConnectionMode(connection.Mode),
				Transport: wire.HostResourceLocalServiceTransport(connection.Transport),
				Target:    connection.Target,
			})
		}
		surfaces := make([]wire.HostResourceExecutionSurface, len(state.Surfaces))
		for i, surface := range state.Surfaces {
			surfaces[i] = wire.HostResourceExecutionSurface(surface)
		}
		items = append(items, wire.HostResource{
			ID: state.ID, Family: state.Family, Label: state.Label, Category: state.Category,
			Description: state.Description, DocsURL: state.DocsURL, Origin: state.Origin,
			Status:      wire.HostResourceStatus(state.Status),
			HostSupport: wire.HostResourceHostSupport(state.HostSupport), Reason: state.Reason,
			Access:        wire.HostResourceAccess(state.Access),
			AccessSetting: wire.HostResourceAccessSetting(state.AccessSetting),
			Prompt:        wire.HostResourcePromptMode(state.Prompt),
			Surfaces:      surfaces, Connections: connections, CheckedAt: state.CheckedAt.Format(time.RFC3339),
		})
	}
	diagnostics := make([]wire.HostResourceDiagnostic, 0, len(snapshot.Diagnostics))
	for _, diagnostic := range snapshot.Diagnostics {
		diagnostics = append(diagnostics, wire.HostResourceDiagnostic{
			Code: diagnostic.Code, Message: diagnostic.Message,
		})
	}
	checkedAt := ""
	if !snapshot.CheckedAt.IsZero() {
		checkedAt = snapshot.CheckedAt.Format(time.RFC3339)
	}
	return wire.HostResourcesResponse{
		Version: snapshot.Version, Resources: items, Diagnostics: diagnostics,
		UserCatalogPath: snapshot.UserCatalogPath, CheckedAt: checkedAt,
		Fingerprint: snapshot.Fingerprint,
	}
}
