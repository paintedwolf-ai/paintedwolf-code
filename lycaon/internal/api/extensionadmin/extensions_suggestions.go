package extensionadmin

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/Masterminds/semver/v3"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/extensionstate"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/projectcontrib"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Handler) HandleGetExtensionSuggestions(w http.ResponseWriter, r *http.Request) {
	projectID, projectDir, ok := requestscope.ProjectIDQuery(s.Projects, s.responses, w, r)
	if !ok {
		return
	}
	p, err := s.Projects.Get(r.Context(), projectID)
	if err == nil && p != nil && !s.Settings.TrustSurfaces.Applies(projectcontrib.SurfaceExtensionSuggestions, *p) {
		httpio.WriteJSON(w, http.StatusOK, wire.ExtensionSuggestionsResponse{
			ProjectID:          projectID,
			SuggestionRevision: extpacks.SuggestionRevision(extpacks.EmptySuggestion()),
			Suggestions:        []wire.ExtensionSuggestion{},
		})
		return
	}
	manifest, err := extpacks.LoadSuggestionFile(extpacks.ProjectDesiredPath(projectDir))
	if err != nil {
		s.writeSuggestionFileError(w, r, err)
		return
	}
	response, err := s.extensionSuggestionsResponse(r, projectID, manifest)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, response)
}

func (s *Handler) extensionSuggestionsResponse(
	r *http.Request,
	projectID string,
	manifest extpacks.SuggestionManifest,
) (wire.ExtensionSuggestionsResponse, error) {
	effective, _, err := s.resolveExtensionsCatalog(r)
	if err != nil {
		return wire.ExtensionSuggestionsResponse{}, err
	}
	return wire.ExtensionSuggestionsResponse{
		ProjectID:          projectID,
		SuggestionRevision: extpacks.SuggestionRevision(manifest),
		Suggestions:        projectSuggestions(manifest, effective),
	}, nil
}

func (s *Handler) HandleAcceptExtensionSuggestions(w http.ResponseWriter, r *http.Request) {
	projectID, projectDir, ok := requestscope.ProjectIDQuery(s.Projects, s.responses, w, r)
	if !ok {
		return
	}
	p, err := s.Projects.Get(r.Context(), projectID)
	if err != nil {
		s.responses.ProjectRegistryError(w, r, err)
		return
	}
	if !requestscope.ProjectSurfaceApplies(s.Settings, p, projectcontrib.SurfaceExtensionSuggestions) {
		s.responses.Fail(w, wire.ApiErrorCodeTrustSurfaceOff,
			"suggested extensions are off — open Trust and turn them on")
		return
	}
	var req wire.ExtensionSuggestionsAcceptRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	if len(req.PackIDs) == 0 {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "pack_ids required")
		return
	}
	seen := make(map[string]struct{}, len(req.PackIDs))
	for i, id := range req.PackIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "pack_ids must be non-empty")
			return
		}
		if _, duplicate := seen[id]; duplicate {
			s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "pack_ids must be unique")
			return
		}
		seen[id] = struct{}{}
		req.PackIDs[i] = id
	}
	manifest, err := extpacks.LoadSuggestionFile(extpacks.ProjectDesiredPath(projectDir))
	if err != nil {
		s.writeSuggestionFileError(w, r, err)
		return
	}
	if req.ExpectedSuggestionRevision == "" || req.ExpectedSuggestionRevision != extpacks.SuggestionRevision(manifest) {
		s.responses.Fail(w, wire.ApiErrorCodeExtensionSuggestionChanged, "project extension suggestions changed after review")
		return
	}
	byID := map[string]extpacks.SuggestedPack{}
	for _, row := range manifest.Suggest {
		byID[strings.TrimSpace(row.ID)] = row
	}
	selected := make([]extpacks.SuggestedPack, 0, len(req.PackIDs))
	for _, id := range req.PackIDs {
		row, found := byID[id]
		if !found {
			s.responses.Fail(w, wire.ApiErrorCodeExtensionSuggestionNotFound, "no suggestion "+id)
			return
		}
		selected = append(selected, row)
	}
	revision := req.ExpectedRevision
	for i, row := range selected {
		id := req.PackIDs[i]
		res, err := s.submitExtensionIntent(r, wire.ExtensionsScopeDevice, "", revision,
			extensionstate.InstallOp{
				Source: row.Source, Version: row.Version, Ref: row.Ref, ExpectedPackID: id,
			})
		if err != nil {
			s.writeExtensionMutationError(w, r, err)
			return
		}
		revision = res.Revision
		res, err = s.submitExtensionIntent(r, wire.ExtensionsScopeDevice, "", revision,
			extensionstate.SetInstalledFromOp{PackID: id, ProjectID: projectID})
		if err != nil {
			s.writeExtensionMutationError(w, r, err)
			return
		}
		revision = res.Revision
	}
	if _, err := s.submitExtensionIntent(r, wire.ExtensionsScopeDevice, "", revision,
		extensionstate.UndeclineOp{PackIDs: req.PackIDs}); err != nil {
		s.writeExtensionMutationError(w, r, err)
		return
	}
	view, err := s.buildExtensionsCatalogViewFresh(r)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, view)
}

func projectSuggestions(manifest extpacks.SuggestionManifest, eff *extpacks.EffectiveCatalog) []wire.ExtensionSuggestion {
	installed := map[string]extpacks.PackSummary{}
	if eff != nil {
		for _, pack := range eff.Packs {
			installed[pack.ID] = pack
		}
	}
	declined := extpacks.DeclinedSet(extpacks.EmptyDesired())
	if eff != nil {
		declined = extpacks.DeclinedSet(eff.Desired)
	}
	out := make([]wire.ExtensionSuggestion, 0, len(manifest.Suggest))
	for _, row := range manifest.Suggest {
		id := strings.TrimSpace(row.ID)
		if id == "" {
			continue
		}
		item := wire.ExtensionSuggestion{
			ID:            id,
			Source:        row.Source,
			Version:       row.Version,
			Ref:           row.Ref,
			Status:        wire.ExtensionSuggestionAvailable,
			Configuration: row.Configuration,
			Contributes:   suggestionContributes(eff, id, row.Version),
		}
		if _, ok := declined[id]; ok {
			item.Status = wire.ExtensionSuggestionDeclined
		}
		if pack, ok := installed[id]; ok && pack.InstallationScope != "stock" {
			item.InstalledVersion = pack.Version
			item.Contributes = suggestionContributes(eff, id, pack.Version)
			if item.Status != wire.ExtensionSuggestionDeclined {
				item.Status = wire.ExtensionSuggestionInstalled
			}
			if versionMismatch(pack.Version, row.Version) {
				item.Status = wire.ExtensionSuggestionVersionMismatch
				item.Contributes = fmt.Sprintf("You're on %s, this project wants %s", pack.Version, displayConstraint(row.Version))
			}
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func displayConstraint(constraint string) string {
	c := strings.TrimSpace(constraint)
	if c == "" {
		return "*"
	}
	return c
}

func versionMismatch(installed, constraint string) bool {
	if strings.TrimSpace(constraint) == "" || strings.TrimSpace(installed) == "" {
		return false
	}
	ver, err := semver.NewVersion(installed)
	if err != nil {
		return false
	}
	want, err := semver.NewConstraint(constraint)
	if err != nil {
		return false
	}
	return !want.Check(ver)
}

func suggestionContributes(eff *extpacks.EffectiveCatalog, packID, version string) string {
	counts := map[string]int{}
	if eff != nil {
		for _, unit := range eff.Units {
			if unit.WinnerPackID != packID || unit.Status != extpacks.UnitStatusLoaded {
				continue
			}
			counts[kindLabel(unit.Kind)]++
		}
	}
	parts := make([]string, 0, 4)
	for _, label := range []string{"Theme", "command", "shortcut", "Workflow", "agent"} {
		n := counts[label]
		if n == 0 {
			continue
		}
		switch label {
		case "command":
			parts = append(parts, fmt.Sprintf("%d %s", n, plural(n, "command", "commands")))
		case "shortcut":
			parts = append(parts, fmt.Sprintf("%d %s", n, plural(n, "shortcut", "shortcuts")))
		default:
			if n == 1 {
				parts = append(parts, label)
			} else {
				parts = append(parts, fmt.Sprintf("%d %ss", n, strings.ToLower(label)))
			}
		}
	}
	summary := strings.Join(parts, ", ")
	if v := strings.TrimSpace(version); v != "" {
		if summary == "" {
			return v
		}
		return summary + " · " + v
	}
	return summary
}

func kindLabel(kind string) string {
	switch {
	case strings.Contains(kind, "theme"):
		return "Theme"
	case strings.Contains(kind, "keybinding"):
		return "shortcut"
	case strings.Contains(kind, "command"):
		return "command"
	case strings.Contains(kind, "workflow"):
		return "Workflow"
	case strings.Contains(kind, "agent"):
		return "agent"
	default:
		return ""
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func (s *Handler) writeSuggestionFileError(w http.ResponseWriter, r *http.Request, err error) {
	var invalid *extpacks.SuggestionInvalidError
	if errors.As(err, &invalid) {
		s.responses.Fail(w, wire.ApiErrorCodeExtensionSuggestionInvalid, "the project's extension suggestions are invalid")
		return
	}
	s.responses.InternalError(w, r, err)
}
