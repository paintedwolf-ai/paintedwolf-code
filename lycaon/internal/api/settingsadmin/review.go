package settingsadmin

import (
	"net/http"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/projectview"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/settings"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Handler) HandleGetReview(w http.ResponseWriter, r *http.Request) {
	scope, ref, err := requestscope.Settings(r, s.Projects)
	projectDir := ref.Dir
	if err != nil {
		requestscope.ScopeError(s.responses, w, r, err)
		return
	}
	cfg := s.Service.Review.Get(scope, projectDir)
	httpio.WriteJSON(w, http.StatusOK, settings.ReviewToDTO(
		settings.WireScope(string(scope)),
		cfg,
		s.Service.Review.MergedFrom(scope, projectDir),
	))
}

func (s *Handler) HandleUpdateReview(w http.ResponseWriter, r *http.Request) {
	scope, ref, err := requestscope.Settings(r, s.Projects)
	projectDir := ref.Dir
	if err != nil {
		requestscope.ScopeError(s.responses, w, r, err)
		return
	}
	var raw map[string]any
	var req wire.UpdateReviewSettingsRequest
	if err := httpio.DecodeJSONWithRaw(w, r, &req, &raw); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	existing := s.Service.Review.Get(scope, projectDir)
	cfg := existing
	if _, ok := raw["review_paths"]; ok {
		if raw["review_paths"] == nil {
			cfg.ReviewPaths = nil
		} else {
			cfg = settings.ReviewFromDTO(req)
		}
	}
	switch scope {
	case llm.SettingsScopeProject:
		if err := s.Service.Review.PutProject(projectDir, cfg); err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
	default:
		if err := s.Service.Review.PutGlobal(cfg); err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
	}
	projectview.PublishSettings(s.Events, s.Projects, r.Context(), wire.SettingsAreaReview, string(scope), projectDir, "updated")
	httpio.WriteJSON(w, http.StatusOK, settings.ReviewToDTO(
		settings.WireScope(string(scope)),
		s.Service.Review.Get(scope, projectDir),
		s.Service.Review.MergedFrom(scope, projectDir),
	))
}
