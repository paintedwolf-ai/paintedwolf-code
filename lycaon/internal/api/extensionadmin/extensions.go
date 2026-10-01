package extensionadmin

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/extensionstate"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/projectcontrib"
	"github.com/lycaon/lycaon/internal/scan"
	wire "github.com/lycaon/lycaon/pkg/api"
	"gopkg.in/yaml.v3"
)

func (s *Handler) extensionsModuleRoot() (string, error) {
	if configlayout.IsModuleRoot(s.ModuleRoot) {
		return s.ModuleRoot, nil
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := wd
	for {
		if configlayout.IsModuleRoot(dir) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			// Release builds use embedded stock content.
			return "", nil
		}
		dir = parent
	}
}

// optionalExtensionsProjectDir resolves project_id when present; empty means device-only.
func (s *Handler) optionalExtensionsProjectDir(r *http.Request) (string, error) {
	if strings.TrimSpace(r.URL.Query().Get("project_id")) == "" {
		return "", nil
	}
	return requestscope.ProjectDir(r, s.Projects)
}

// extensionUnitWriteTarget resolves unit-disable scope.
func (s *Handler) extensionUnitWriteTarget(r *http.Request) (wire.ExtensionsDesiredScope, string, error) {
	scope := wire.ExtensionsDesiredScope(strings.TrimSpace(r.URL.Query().Get("scope")))
	dir, err := s.extensionsScopeProjectDir(r, scope)
	if err != nil {
		return "", "", err
	}
	if scope == "" {
		scope = wire.ExtensionsScopeDevice
	}
	return scope, dir, nil
}

func (s *Handler) extensionsScopeProjectDir(r *http.Request, scope wire.ExtensionsDesiredScope) (string, error) {
	switch scope {
	case wire.ExtensionsScopeDevice, "":
		return "", nil
	case wire.ExtensionsScopeProject:
		return requestscope.ProjectDir(r, s.Projects)
	default:
		return "", &httpio.QueryParameterError{Parameter: "scope", Reason: "must be project or device"}
	}
}

// gatedExtensionsProjectDir applies the project Trust switch.
func (s *Handler) gatedExtensionsProjectDir(r *http.Request, projectID, projectDir string) (dir string, withheld bool) {
	if projectID == "" {
		return "", true
	}
	p, err := s.Projects.Get(r.Context(), projectID)
	if err != nil || p == nil {
		return "", true
	}
	if !s.Settings.TrustSurfaces.Applies(projectcontrib.SurfaceExtensionConfig, *p) {
		return "", true
	}
	return projectDir, false
}

// resolveExtensionsCatalog only reads: journal recovery runs at boot and ahead
// of every owner mutation.
func (s *Handler) resolveExtensionsCatalog(r *http.Request) (*extpacks.EffectiveCatalog, string, error) {
	root, err := s.extensionsModuleRoot()
	if err != nil {
		return nil, "", err
	}
	requestedDir, err := s.optionalExtensionsProjectDir(r)
	if err != nil {
		return nil, "", err
	}
	projectDir, withheld := requestedDir, false
	if requestedDir != "" {
		projectDir, withheld = s.gatedExtensionsProjectDir(
			r, strings.TrimSpace(r.URL.Query().Get("project_id")), requestedDir)
	}
	homeDir, err := configdir.UserConfigDir()
	if err != nil {
		return nil, "", err
	}
	// An empty root selects embedded stock content.
	scanners := scan.RequirementChecker{ModuleRoot: root, HomeDir: homeDir}
	resolved, err := extpacks.ResolveCatalog(r.Context(), []string{projectDir}, scanners)
	if err != nil {
		return nil, "", err
	}
	// Return only contributions the runtime applies.
	eff := s.Owner.Views.InForce(r.Context(), resolved)
	if withheld {
		if diag, ok := extpacks.ProjectDesiredWithheldDiagnostic(requestedDir); ok {
			eff.Diagnostics = append(eff.Diagnostics, diag)
		}
	}
	return eff, projectDir, nil
}

func wireDesired(d extpacks.DesiredState, yamlText, note string) wire.ExtensionDesiredState {
	out := wire.ExtensionDesiredState{
		Format:    d.Format,
		Disabled:  append([]string{}, d.Disabled...),
		Own:       map[string]string{},
		YAML:      yamlText,
		ScopeNote: note,
		Packs:     []wire.ExtensionDesiredPack{},
	}
	if d.Own != nil {
		for k, v := range d.Own {
			out.Own[k] = v
		}
	}
	if d.Configuration != nil {
		out.Configuration = make(map[string]map[string]any, len(d.Configuration))
		for packID, properties := range d.Configuration {
			values := make(map[string]any, len(properties))
			for name, value := range properties {
				values[name] = value
			}
			out.Configuration[packID] = values
		}
	}
	if len(d.Declined) > 0 {
		out.Declined = append([]string{}, d.Declined...)
	}
	for _, p := range d.Packs {
		row := wire.ExtensionDesiredPack{
			ID: p.ID, Source: p.Source, Version: p.Version, Ref: p.Ref,
			Development: p.Development, Enabled: p.Enabled, InstalledFrom: p.InstalledFrom,
		}
		out.Packs = append(out.Packs, row)
	}
	return out
}

func desiredToWire(d extpacks.DesiredState, note string) (wire.ExtensionDesiredState, error) {
	data, err := yaml.Marshal(&d)
	if err != nil {
		return wire.ExtensionDesiredState{}, err
	}
	return wireDesired(d, string(data), note), nil
}

func unitSummaryWire(u extpacks.UnitEffective, includeBodies bool) wire.ExtensionUnitSummary {
	out := wire.ExtensionUnitSummary{
		ProjectDisableAllowed: extpacks.ProjectDisableAllowed(u.Kind, len(u.Contributions) > 0),
		ID:                    u.ID,
		Kind:                  u.Kind,
		Title:                 u.Title,
		Status:                wire.ExtensionUnitStatus(u.Status),
		WinnerPackID:          u.WinnerPackID,
		Contributions:         make([]wire.ExtensionUnitContribution, 0, len(u.Contributions)),
	}
	for _, p := range u.Contributions {
		prov := wire.ExtensionUnitContribution{PackID: p.PackID, Path: p.Path.String()}
		if includeBodies {
			prov.Content = string(p.Content)
		}
		out.Contributions = append(out.Contributions, prov)
	}
	return out
}

func sortedUnitIDs(eff *extpacks.EffectiveCatalog) []string {
	ids := make([]string, 0, len(eff.Units))
	for id := range eff.Units {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func (s *Handler) HandleGetExtensionUnit(w http.ResponseWriter, r *http.Request) {
	id := httpio.EncodedPathID(r, "unit_id")
	if id == "" {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "unit_id required")
		return
	}
	eff, _, err := s.resolveExtensionsCatalog(r)
	if err != nil {
		requestscope.ScopeError(s.responses, w, r, err)
		return
	}
	u, ok := eff.Units[id]
	if !ok {
		s.responses.Fail(w, wire.ApiErrorCodeExtensionUnitNotFound, "unit not found")
		return
	}
	sum := unitSummaryWire(u, true)
	detail := wire.ExtensionUnitDetail{
		ProjectDisableAllowed: sum.ProjectDisableAllowed,
		ID:                    sum.ID, Kind: sum.Kind, Title: sum.Title, Status: sum.Status,
		WinnerPackID: sum.WinnerPackID, Contributions: sum.Contributions,
		Content: string(u.Content),
	}
	httpio.WriteJSON(w, http.StatusOK, detail)
}

func (s *Handler) HandleInstallExtensionPack(w http.ResponseWriter, r *http.Request) {
	var req wire.ExtensionInstallRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	res, err := s.submitExtensionIntent(r, wire.ExtensionsScopeDevice, "", req.ExpectedRevision,
		extensionstate.InstallOp{Source: req.Source, Version: req.Version, Ref: req.Ref})
	if err != nil {
		s.writeExtensionMutationError(w, r, err)
		return
	}
	view, err := s.buildExtensionsCatalogViewFresh(r)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	out := wire.ExtensionInstallResponse{View: view, Warnings: res.Warnings}
	if res.Install != nil {
		out.PackID = res.Install.PackID
		out.PackageRoot = res.PackageRoot
		out.ResolvedRevision = res.Install.ResolvedRevision
		out.Version = res.Install.Version
	}
	httpio.WriteJSON(w, http.StatusCreated, out)
}

func (s *Handler) HandleDeleteExtensionPack(w http.ResponseWriter, r *http.Request) {
	packID := httpio.EncodedPathID(r, "pack_id")
	if packID == "" {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "pack_id required")
		return
	}
	if err := extpacks.ValidatePackID(packID); err != nil {
		s.responses.FailDetails(w, wire.ApiErrorCodeInvalidRequest, map[string]any{"field": "pack_id"}, "invalid pack_id")
		return
	}
	expectedRevision := strings.TrimSpace(r.URL.Query().Get("expected_revision"))
	if expectedRevision == "" {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "expected_revision required")
		return
	}
	if !s.requireExtensionPack(w, r, packID) {
		return
	}
	_, err := s.submitExtensionIntent(r, wire.ExtensionsScopeDevice, "", expectedRevision,
		extensionstate.RemoveOp{PackID: packID})
	if err != nil {
		s.writeExtensionMutationError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeExtensionMutation returns the committed catalog view.
func (s *Handler) writeExtensionMutation(w http.ResponseWriter, r *http.Request, res extensionstate.Result) {
	view, err := s.buildExtensionsCatalogViewFresh(r)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, wire.ExtensionMutationResponse{View: view, Warnings: res.Warnings})
}

func (s *Handler) HandleUpdateExtensionPack(w http.ResponseWriter, r *http.Request) {
	packID := httpio.EncodedPathID(r, "pack_id")
	if packID == "" {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "pack_id required")
		return
	}
	if err := extpacks.ValidatePackID(packID); err != nil {
		s.responses.FailDetails(w, wire.ApiErrorCodeInvalidRequest, map[string]any{"field": "pack_id"}, "invalid pack_id")
		return
	}
	var req wire.UpdateExtensionPackRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	if req.Enabled == nil {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "enabled required")
		return
	}
	if !s.requireExtensionPack(w, r, packID) {
		return
	}
	res, err := s.submitExtensionIntent(r, wire.ExtensionsScopeDevice, "", req.ExpectedRevision,
		extensionstate.SetPackEnabledOp{PackID: packID, Enabled: *req.Enabled})
	if err != nil {
		s.writeExtensionMutationError(w, r, err)
		return
	}
	s.writeExtensionMutation(w, r, res)
}

func (s *Handler) HandleUpdateExtensionUnit(w http.ResponseWriter, r *http.Request) {
	unitID := httpio.EncodedPathID(r, "unit_id")
	if unitID == "" {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "unit_id required")
		return
	}
	var req wire.UpdateExtensionUnitRequest
	var raw map[string]any
	if err := httpio.DecodeJSONWithRaw(w, r, &req, &raw); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	// A null own_pack_id clears the selection; an absent one leaves it.
	_, ownSet := raw["own_pack_id"]
	if req.Enabled == nil && !ownSet {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "either enabled or own_pack_id required")
		return
	}
	scope, projectDir, err := s.extensionUnitWriteTarget(r)
	if err != nil {
		requestscope.ScopeError(s.responses, w, r, err)
		return
	}
	if !s.requireExtensionUnit(w, r, unitID) {
		return
	}
	ownPackID := ""
	if req.OwnPackID != nil {
		ownPackID = strings.TrimSpace(*req.OwnPackID)
	}
	if ownSet && !s.checkUnitOwnChoice(w, r, scope, unitID, ownPackID) {
		return
	}
	res, err := s.submitExtensionIntent(r, scope, projectDir, req.ExpectedRevision,
		extensionstate.UpdateUnitOp{UnitID: unitID, Enabled: req.Enabled, OwnSet: ownSet, OwnPackID: ownPackID})
	if err != nil {
		s.writeExtensionMutationError(w, r, err)
		return
	}

	s.writeExtensionMutation(w, r, res)
}

// checkUnitOwnChoice answers whether own_pack_id may be applied to unitID in
// scope, writing the refusal when it may not.
func (s *Handler) checkUnitOwnChoice(w http.ResponseWriter, r *http.Request, scope wire.ExtensionsDesiredScope, unitID, ownPackID string) bool {
	if scope == wire.ExtensionsScopeProject {
		s.writeExtensionMutationError(w, r, extensionstate.ErrProjectMutationUnsupported)
		return false
	}
	winner := strings.TrimSpace(ownPackID)
	if winner == "" {
		return true
	}
	eff, _, err := s.resolveExtensionsCatalog(r)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return false
	}
	contributions := eff.InspectContributions(unitID)
	if len(contributions) == 0 {
		s.writeExtensionMutationError(w, r, extensionstate.ErrUnknownUnit)
		return false
	}
	if extpacks.ProviderScoped(extpacks.KindRootForUnitID(unitID)) {
		s.responses.FailDetails(w, wire.ApiErrorCodeInvalidRequest, map[string]any{"field": "own_pack_id"},
			"this unit belongs to the pack that declares it; disable it instead")
		return false
	}
	for _, c := range contributions {
		if c.PackID == winner {
			return true
		}
	}
	s.responses.FailDetails(w, wire.ApiErrorCodeInvalidRequest, map[string]any{"field": "own_pack_id"},
		"that pack does not provide this unit")
	return false
}

func (s *Handler) HandleUpdateExtensionConfiguration(w http.ResponseWriter, r *http.Request) {
	var req wire.ExtensionConfigurationRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	res, err := s.submitExtensionIntent(r, wire.ExtensionsScopeDevice, "", req.ExpectedRevision,
		extensionstate.SetConfigurationOp{Packs: req.Packs})
	if err != nil {
		s.writeExtensionMutationError(w, r, err)
		return
	}
	s.writeExtensionMutation(w, r, res)
}

func (s *Handler) HandleApplyExtensionPackProfile(w http.ResponseWriter, r *http.Request) {
	packID := httpio.EncodedPathID(r, "pack_id")
	if packID == "" {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "pack_id required")
		return
	}
	profileName := httpio.EncodedPathID(r, "profile_name")
	if profileName == "" {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "profile_name required")
		return
	}
	var req wire.ExtensionRevisionRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	if !s.requireExtensionProfile(w, r, packID, profileName) {
		return
	}
	res, err := s.submitExtensionIntent(r, wire.ExtensionsScopeDevice, "", req.ExpectedRevision,
		extensionstate.ApplyProfileOp{PackID: packID, Profile: profileName})
	if err != nil {
		s.writeExtensionMutationError(w, r, err)
		return
	}
	s.writeExtensionMutation(w, r, res)
}

func (s *Handler) HandleGetExtensionPackUpdate(w http.ResponseWriter, r *http.Request) {
	packID := httpio.EncodedPathID(r, "pack_id")
	if packID == "" {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "pack_id required")
		return
	}
	res, err := extpacks.CheckUpdate(r.Context(), packID)
	var unresolved *extpacks.ResolutionError
	switch {
	case errors.Is(err, extpacks.ErrPackNotInstalled):
		s.responses.Fail(w, wire.ApiErrorCodeExtensionPackNotFound, "extension pack not installed")
		return
	case errors.As(err, &unresolved):
		s.responses.Logger.InfoContext(r.Context(), "extension pack update unresolved", "pack_id", packID, "err", unresolved)
		s.responses.FailReason(w, wire.ApiErrorCodeExtensionCandidateRejected, "No compatible release of this pack could be resolved.")
		return
	case err != nil:
		s.responses.InternalError(w, r, err)
		return
	}
	changes := make([]wire.ExtensionPackageChange, 0, len(res.Changes))
	for _, change := range res.Changes {
		changes = append(changes, wire.ExtensionPackageChange{
			PackID: change.PackID, CurrentVersion: change.CurrentVersion,
			CandidateVersion: change.CandidateVersion, CurrentRevision: change.CurrentRevision,
			CandidateRevision: change.CandidateRevision, Kind: change.Kind,
		})
	}
	httpio.WriteJSON(w, http.StatusOK, wire.ExtensionPackUpdateStatus{
		PackID:            res.PackID,
		Available:         res.Available,
		CurrentVersion:    res.CurrentVersion,
		CandidateVersion:  res.CandidateVersion,
		CurrentRevision:   res.CurrentRevision,
		CandidateRevision: res.CandidateRevision,
		Changes:           changes,
		Message:           res.Message,
	})
}

func (s *Handler) HandleApplyExtensionPackUpdate(w http.ResponseWriter, r *http.Request) {
	packID := httpio.EncodedPathID(r, "pack_id")
	if packID == "" {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "pack_id required")
		return
	}
	var req wire.ExtensionRevisionRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	if !s.requireExtensionPack(w, r, packID) {
		return
	}
	res, err := s.submitExtensionIntent(r, wire.ExtensionsScopeDevice, "", req.ExpectedRevision,
		extensionstate.UpdateOp{PackID: packID})
	if err != nil {
		s.writeExtensionMutationError(w, r, err)
		return
	}
	view, err := s.buildExtensionsCatalogViewFresh(r)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	out := wire.ExtensionPackActionResponse{View: view, PackID: packID, Message: "updated"}
	if res.Install != nil {
		out.ResolvedRevision = res.Install.ResolvedRevision
		out.Version = res.Install.Version
	}
	httpio.WriteJSON(w, http.StatusOK, out)
}

func (s *Handler) HandleReloadExtensionPack(w http.ResponseWriter, r *http.Request) {
	packID := httpio.EncodedPathID(r, "pack_id")
	if packID == "" {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "pack_id required")
		return
	}
	var req wire.ExtensionRevisionRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	if !s.requireExtensionPack(w, r, packID) {
		return
	}
	res, err := s.submitExtensionIntent(r, wire.ExtensionsScopeDevice, "", req.ExpectedRevision,
		extensionstate.ReloadOp{PackID: packID})
	if err != nil {
		s.writeExtensionMutationError(w, r, err)
		return
	}
	view, err := s.buildExtensionsCatalogViewFresh(r)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	out := wire.ExtensionPackActionResponse{View: view, PackID: packID, Warnings: res.Warnings, Message: "reloaded"}
	if res.Install != nil {
		out.Version = res.Install.Version
	}
	httpio.WriteJSON(w, http.StatusOK, out)
}
