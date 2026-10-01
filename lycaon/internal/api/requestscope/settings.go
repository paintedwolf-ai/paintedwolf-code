package requestscope

import (
	"errors"
	"net/http"
	"strings"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/settings"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// Settings retains both project identity and overlay path.
func Settings(r *http.Request, registry project.Registry) (llm.SettingsScope, settings.ProjectRef, error) {
	scopeParam := strings.TrimSpace(r.URL.Query().Get("scope"))
	if scopeParam != "" && scopeParam != string(llm.SettingsScopeGlobal) && scopeParam != string(llm.SettingsScopeProject) {
		return "", settings.ProjectRef{}, ErrInvalidScope
	}
	projectID := strings.TrimSpace(r.URL.Query().Get("project_id"))
	if projectID != "" || scopeParam == string(llm.SettingsScopeProject) {
		if projectID == "" {
			return "", settings.ProjectRef{}, ErrProjectRequired
		}
		p, err := registry.Get(r.Context(), projectID)
		if err != nil {
			return "", settings.ProjectRef{}, err
		}
		overlay, err := project.ResolveProjectOverlay(p, "")
		if err != nil {
			return "", settings.ProjectRef{}, err
		}
		if err := overlay.CheckCompatibility(); err != nil {
			return "", settings.ProjectRef{}, err
		}
		return llm.SettingsScopeProject, settings.ProjectRef{ID: projectID, Dir: overlay.Primary.Path}, nil
	}
	return llm.SettingsScopeGlobal, settings.ProjectRef{}, nil
}

var ErrInvalidScope = &httpio.QueryParameterError{Parameter: "scope", Reason: "must be global or project"}

var ErrProjectRequired = &httpio.QueryParameterError{Parameter: "project_id", Reason: "is required for project scope"}

// ProjectDir resolves project_id to path for settings handlers.
func ProjectDir(r *http.Request, registry project.Registry) (string, error) {
	projectID := strings.TrimSpace(r.URL.Query().Get("project_id"))
	if projectID == "" {
		return "", ErrProjectRequired
	}
	p, err := registry.Get(r.Context(), projectID)
	if err != nil {
		return "", err
	}
	overlay, err := project.ResolveProjectOverlay(p, "")
	if err != nil {
		return "", err
	}
	if err := overlay.CheckCompatibility(); err != nil {
		return "", err
	}
	return overlay.Primary.Path, nil
}

// ScopeError answers a failed project scope resolution: a bad scope query, an
// unknown project, or an overlay this build cannot read. Anything else is an
// internal failure.
func ScopeError(responses *httpio.Responder, w http.ResponseWriter, r *http.Request, err error) {
	var queryErr *httpio.QueryParameterError
	switch {
	case errors.As(err, &queryErr):
		responses.InvalidQuery(w, err)
	case errors.Is(err, project.ErrNotFound):
		responses.Fail(w, wire.ApiErrorCodeProjectNotFound, "project not found")
	case responses.OverlayFormatError(w, err):
	default:
		responses.InternalError(w, r, err)
	}
}
