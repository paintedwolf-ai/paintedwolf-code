package settingsadmin

import (
	"net/http"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/projectview"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Handler) HandleGetFileSummariesSettings(w http.ResponseWriter, _ *http.Request) {
	httpio.WriteJSON(w, http.StatusOK, wire.FileSummariesSettingsResponse{
		Enabled: s.Service.FileSummaries.Enabled(),
	})
}

func (s *Handler) HandleUpdateFileSummariesSettings(w http.ResponseWriter, r *http.Request) {
	var raw map[string]any
	var req wire.UpdateFileSummariesSettingsRequest
	if err := httpio.DecodeJSONWithRaw(w, r, &req, &raw); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	enabled := s.Service.FileSummaries.Enabled()
	if _, ok := raw["enabled"]; ok {
		if raw["enabled"] == nil {
			enabled = true
		} else {
			enabled = req.Enabled
		}
		if err := s.Sources.Briefings.FileBriefings.SetEnabled(r.Context(), enabled); err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
		projectview.PublishSettings(s.Events, s.Projects, r.Context(), wire.SettingsAreaFileSummaries, "global", "", "updated")
	}
	httpio.WriteJSON(w, http.StatusOK, wire.FileSummariesSettingsResponse{Enabled: enabled})
}
