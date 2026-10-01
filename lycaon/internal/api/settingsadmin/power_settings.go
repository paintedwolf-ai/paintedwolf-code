package settingsadmin

import (
	"net/http"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/projectview"
	"github.com/lycaon/lycaon/internal/hostpower"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Handler) HandleGetPowerSettings(w http.ResponseWriter, _ *http.Request) {
	httpio.WriteJSON(w, http.StatusOK, powerSettingsResponse(s.Power.Snapshot()))
}

func (s *Handler) HandleUpdatePowerSettings(w http.ResponseWriter, r *http.Request) {
	var raw map[string]any
	var req wire.UpdatePowerSettingsRequest
	if err := httpio.DecodeJSONWithRaw(w, r, &req, &raw); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	if _, ok := raw["keep_awake_while_working"]; ok {
		enabled := false
		if raw["keep_awake_while_working"] != nil {
			enabled = req.KeepAwakeWhileWorking
		}
		if err := s.Service.Power.PutKeepAwakeWhileWorking(enabled); err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
		s.Power.SetEnabled(enabled)
		projectview.PublishSettings(s.Events, s.Projects, r.Context(), wire.SettingsAreaPower, "global", "", "updated")
	}
	httpio.WriteJSON(w, http.StatusOK, powerSettingsResponse(s.Power.Snapshot()))
}

func powerSettingsResponse(status hostpower.Status) wire.PowerSettingsResponse {
	return wire.PowerSettingsResponse{
		KeepAwakeWhileWorking: status.Enabled,
		Supported:             status.Supported,
		Inhibiting:            status.Inhibiting,
		ActiveWorkCount:       status.ActiveWorkCount,
		LastError:             status.LastError,
	}
}
