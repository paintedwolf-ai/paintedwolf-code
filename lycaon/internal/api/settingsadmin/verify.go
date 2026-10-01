package settingsadmin

import (
	"net/http"
	"time"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/projectview"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/settings"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Handler) HandleGetVerify(w http.ResponseWriter, r *http.Request) {
	scope, ref, err := requestscope.Settings(r, s.Projects)
	if err != nil {
		requestscope.ScopeError(s.responses, w, r, err)
		return
	}
	if scope != llm.SettingsScopeProject {
		s.responses.InvalidQuery(w, requestscope.ErrProjectRequired)
		return
	}
	projectDir := ref.Dir
	cfg := s.Service.Verify.Get(scope, projectDir)
	detect, hasDetect := s.Service.Verify.ProposalFor(projectDir)
	httpio.WriteJSON(w, http.StatusOK, settings.VerifyToDTO(
		settings.WireScope(string(scope)),
		projectDir,
		cfg,
		detect,
		hasDetect,
	))
}

func (s *Handler) HandleUpdateVerify(w http.ResponseWriter, r *http.Request) {
	scope, ref, err := requestscope.Settings(r, s.Projects)
	if err != nil {
		requestscope.ScopeError(s.responses, w, r, err)
		return
	}
	if scope != llm.SettingsScopeProject {
		s.responses.InvalidQuery(w, requestscope.ErrProjectRequired)
		return
	}
	projectDir := ref.Dir
	var raw map[string]any
	var req wire.UpdateVerifySettingsRequest
	if err := httpio.DecodeJSONWithRaw(w, r, &req, &raw); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	if !s.responses.RequireJSONObjectKeys(w, raw, "test") {
		return
	}
	cfg := settings.VerifyFromDTO(req)
	if err := s.Service.Verify.PutProject(projectDir, cfg); err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	projectview.PublishSettings(s.Events, s.Projects, r.Context(), wire.SettingsAreaVerify, string(scope), projectDir, "updated")
	detect, hasDetect := s.Service.Verify.ProposalFor(projectDir)
	httpio.WriteJSON(w, http.StatusOK, settings.VerifyToDTO(
		settings.WireScope(string(scope)),
		projectDir,
		s.Service.Verify.Get(scope, projectDir),
		detect,
		hasDetect,
	))
}

func (s *Handler) HandleDismissVerify(w http.ResponseWriter, r *http.Request) {
	scope, ref, err := requestscope.Settings(r, s.Projects)
	if err != nil {
		requestscope.ScopeError(s.responses, w, r, err)
		return
	}
	if scope != llm.SettingsScopeProject {
		s.responses.InvalidQuery(w, requestscope.ErrProjectRequired)
		return
	}
	projectDir := ref.Dir
	var raw map[string]any
	var req wire.DismissVerifySettingsRequest
	if err := httpio.DecodeJSONWithRaw(w, r, &req, &raw); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	if !s.responses.RequireJSONObjectKeys(w, raw, "dismissed") {
		return
	}
	if req.Dismissed {
		s.Service.Verify.DismissProposal(projectDir, time.Now().UTC().Format(time.RFC3339))
	} else {
		s.Service.Verify.UndismissProposal(projectDir)
	}
	projectview.PublishSettings(s.Events, s.Projects, r.Context(), wire.SettingsAreaVerify, string(scope), projectDir, "updated")
	detect, hasDetect := s.Service.Verify.ProposalFor(projectDir)
	httpio.WriteJSON(w, http.StatusOK, settings.VerifyToDTO(
		settings.WireScope(string(scope)),
		projectDir,
		s.Service.Verify.Get(scope, projectDir),
		detect,
		hasDetect,
	))
}
