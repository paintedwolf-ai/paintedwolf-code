package settingsadmin

import (
	"net/http"
	"strings"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/projectview"
	"github.com/lycaon/lycaon/internal/settings"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Handler) HandleGetSecurityScannersSettings(w http.ResponseWriter, r *http.Request) {
	cfg := s.Service.SecurityScanners.Effective()
	httpio.WriteJSON(w, http.StatusOK, settings.SecurityScannersToDTO(cfg, s.Service.SecurityScanners.MergedFrom()))
}

func (s *Handler) HandleUpdateSecurityScannersSettings(w http.ResponseWriter, r *http.Request) {
	var raw map[string]any
	var req wire.UpdateSecurityScannersSettingsRequest
	if err := httpio.DecodeJSONWithRaw(w, r, &req, &raw); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}

	overlay := s.Service.SecurityScanners.UserOverlay()

	if _, ok := raw["enabled"]; ok {
		if raw["enabled"] == nil {
			overlay.Enabled = nil
		} else {
			v := req.Enabled
			overlay.Enabled = &v
		}
	}
	if _, ok := raw["landed_change_scope"]; ok {
		if raw["landed_change_scope"] == nil {
			overlay.LandedChangeScope = nil
		} else {
			if msg := settings.ValidateLandedChangeScope(string(req.LandedChangeScope)); msg != "" {
				s.responses.FailDetails(w, wire.ApiErrorCodeInvalidRequest, map[string]any{"field": "landed_change_scope"}, msg)
				return
			}
			v := strings.TrimSpace(string(req.LandedChangeScope))
			overlay.LandedChangeScope = &v
		}
	}
	if _, ok := raw["source_verify"]; ok {
		if raw["source_verify"] == nil {
			overlay.SourceVerify = nil
		} else {
			if msg := settings.ValidateSourceVerify(string(req.SourceVerify)); msg != "" {
				s.responses.FailDetails(w, wire.ApiErrorCodeInvalidRequest, map[string]any{"field": "source_verify"}, msg)
				return
			}
			v := strings.TrimSpace(string(req.SourceVerify))
			overlay.SourceVerify = &v
		}
	}

	if err := s.Service.SecurityScanners.PutGlobal(overlay); err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	projectview.PublishSettings(s.Events, s.Projects, r.Context(), wire.SettingsAreaSecurityScanners, "global", "", "updated")
	cfg := s.Service.SecurityScanners.Effective()
	httpio.WriteJSON(w, http.StatusOK, settings.SecurityScannersToDTO(cfg, s.Service.SecurityScanners.MergedFrom()))
}
