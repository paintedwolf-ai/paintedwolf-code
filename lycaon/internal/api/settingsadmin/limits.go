package settingsadmin

import (
	"net/http"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/projectview"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/settings"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Handler) HandleGetLimits(w http.ResponseWriter, r *http.Request) {
	scope, ref, err := requestscope.Settings(r, s.Projects)
	projectDir := ref.Dir
	if err != nil {
		requestscope.ScopeError(s.responses, w, r, err)
		return
	}
	lim := s.Service.Limits.Get(scope, projectDir)
	// Fill unset display fields from the default model window.
	lim = settings.ApplyDerivedSessionLimits(lim, llm.ScaleLimitsFromTrueWindow(modelinfo.DefaultFallbackTrueWindow))
	httpio.WriteJSON(w, http.StatusOK, settings.LimitsToDTO(
		settings.WireScope(string(scope)),
		lim,
		s.Service.Limits.MergedFrom(scope, projectDir),
	))
}

func (s *Handler) HandleUpdateLimits(w http.ResponseWriter, r *http.Request) {
	scope, ref, err := requestscope.Settings(r, s.Projects)
	projectDir := ref.Dir
	if err != nil {
		requestscope.ScopeError(s.responses, w, r, err)
		return
	}
	var raw map[string]any
	var req wire.SettingsLimitsPatch
	if err := httpio.DecodeJSONWithRaw(w, r, &req, &raw); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	if field, msg := validateLimitsPatch(raw, req); msg != "" {
		s.responses.FailDetails(w, wire.ApiErrorCodeInvalidRequest, map[string]any{"field": field}, msg)
		return
	}

	current := s.Service.Limits.Get(scope, projectDir)
	var base settings.SessionLimits
	if scope == llm.SettingsScopeProject {
		base = s.Service.Limits.Get(llm.SettingsScopeGlobal, "")
	} else {
		base = settings.DefaultSessionLimits()
	}
	lim := applyLimitsPatch(current, base, raw, req)

	switch scope {
	case llm.SettingsScopeProject:
		lim = settings.ProjectSpendOverlay(lim, s.Service.Limits.Get(llm.SettingsScopeGlobal, ""))
		if err := s.Service.Limits.PutProject(projectDir, lim); err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
	default:
		if err := s.Service.Limits.PutGlobal(lim); err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
	}
	projectview.PublishSettings(s.Events, s.Projects, r.Context(), wire.SettingsAreaLimits, string(scope), projectDir, "updated")
	httpio.WriteJSON(w, http.StatusOK, settings.LimitsToDTO(
		settings.WireScope(string(scope)),
		s.Service.Limits.Get(scope, projectDir),
		s.Service.Limits.MergedFrom(scope, projectDir),
	))
}
