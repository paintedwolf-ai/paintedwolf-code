package extensionadmin

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/extensionstate"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/project"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func packSummaryWire(p extpacks.PackSummary) wire.ExtensionPackSummary {
	ids := p.MetaPackIDs
	if ids == nil {
		ids = []string{}
	}
	return wire.ExtensionPackSummary{
		ID:                    p.ID,
		Name:                  p.Name,
		Version:               p.Version,
		VersionConstraint:     p.VersionConstraint,
		ResolvedRevision:      p.ResolvedRevision,
		Integrity:             p.Integrity,
		ExtensionAPI:          p.ExtensionAPI,
		InstallationState:     p.InstallationState,
		InstallationScope:     p.InstallationScope,
		Dependencies:          p.Dependencies,
		DependencyOf:          append([]string(nil), p.DependencyOf...),
		Source:                p.Source,
		Ref:                   p.Ref,
		Kind:                  wire.ExtensionPackKind(p.Kind),
		Enabled:               p.Enabled,
		Removable:             p.Removable,
		UnitCount:             p.UnitCount,
		HasProfile:            p.HasProfile,
		BlockedReason:         wire.ExtensionBlockedReason(p.BlockedReason),
		UnmetRequiresScanners: append([]string(nil), p.UnmetRequiresScanners...),
		Contributing:          p.Contributing,
		NeedsReload:           p.NeedsReload,
		Feature:               p.Feature,
		MetaPackIDs:           ids,
	}
}

func (s *Handler) annotatePackProvenance(ctx context.Context, view *wire.ExtensionsCatalogView) {
	from := map[string]string{}
	for _, pack := range view.Desired.Packs {
		if pack.InstalledFrom != "" {
			from[pack.ID] = pack.InstalledFrom
		}
	}
	refs := s.suggestionReferenceCounts(ctx)
	for i := range view.Packs {
		view.Packs[i].InstalledFrom = from[view.Packs[i].ID]
		view.Packs[i].ReferencedBy = refs[view.Packs[i].ID]
	}
}

func (s *Handler) suggestionReferenceCounts(ctx context.Context) map[string]int {
	out := map[string]int{}
	projects, err := s.Projects.List(ctx)
	if err != nil {
		return out
	}
	for i := range projects {
		p := projects[i]
		dir := project.PrimaryRootPath(&p)
		if dir == "" {
			continue
		}
		manifest, err := extpacks.LoadSuggestionFile(extpacks.ProjectDesiredPath(dir))
		if err != nil {
			continue
		}
		seen := map[string]struct{}{}
		for _, row := range manifest.Suggest {
			id := strings.TrimSpace(row.ID)
			if id == "" {
				continue
			}
			if _, dup := seen[id]; dup {
				continue
			}
			seen[id] = struct{}{}
			out[id]++
		}
	}
	return out
}

func metaPackSummaryWire(m extpacks.MetaPackSummary) wire.ExtensionMetaPackSummary {
	diags := make([]wire.ExtensionDiagnostic, 0, len(m.Diagnostics))
	for _, d := range m.Diagnostics {
		diags = append(diags, extensionDiagnosticWire(d))
	}
	return wire.ExtensionMetaPackSummary{
		ID:            m.ID,
		Name:          m.Name,
		Version:       m.Version,
		Kind:          wire.ExtensionPackKind(m.Kind),
		Status:        wire.ExtensionMetaPackStatus(m.Status),
		Members:       append([]string{}, m.Members...),
		ConflictsWith: append([]string{}, m.ConflictsWith...),
		Extends:       append([]string{}, m.Extends...),
		Diagnostics:   diags,
		Removable:     m.Removable,
	}
}

// buildExtensionsCatalogView serves cached reads.
func (s *Handler) buildExtensionsCatalogView(r *http.Request) (wire.ExtensionsCatalogView, error) {
	projectDir, err := s.optionalExtensionsProjectDir(r)
	if err != nil {
		return wire.ExtensionsCatalogView{}, err
	}
	if cached, ok := s.extensionsOverview.get(projectDir); ok {
		return cached, nil
	}
	return s.buildExtensionsCatalogViewFresh(r)
}

// buildExtensionsCatalogViewFresh refreshes mutation responses.
func (s *Handler) buildExtensionsCatalogViewFresh(r *http.Request) (wire.ExtensionsCatalogView, error) {
	projectDir, err := s.optionalExtensionsProjectDir(r)
	if err != nil {
		return wire.ExtensionsCatalogView{}, err
	}
	eff, _, err := s.resolveExtensionsCatalog(r)
	if err != nil {
		return wire.ExtensionsCatalogView{}, err
	}
	metas, discoverDiags, err := extpacks.DiscoverMetaPacks()
	if err != nil {
		return wire.ExtensionsCatalogView{}, err
	}
	revision, err := s.currentExtensionRevision(projectDir)
	if err != nil {
		return wire.ExtensionsCatalogView{}, err
	}
	desired, desiredPath, err := s.mergedDesiredWire(projectDir)
	if err != nil {
		return wire.ExtensionsCatalogView{}, err
	}
	out := wire.ExtensionsCatalogView{
		OK:          true,
		Revision:    revision,
		Packs:       []wire.ExtensionPackSummary{},
		MetaPacks:   []wire.ExtensionMetaPackSummary{},
		Diagnostics: []wire.ExtensionDiagnostic{},
		Units:       []wire.ExtensionUnitSummary{},
		Desired:     desired,
		DesiredPath: desiredPath,
	}
	for _, p := range extpacks.AttachMetaPackIDs(eff.Packs, metas) {
		out.Packs = append(out.Packs, packSummaryWire(p))
	}
	for _, m := range extpacks.BuildMetaPackSummaries(eff, metas, eff.Desired) {
		out.MetaPacks = append(out.MetaPacks, metaPackSummaryWire(m))
	}
	catalogDiags := append(append([]extpacks.Diagnostic{}, eff.Diagnostics...), discoverDiags...)
	for _, d := range catalogDiags {
		out.Diagnostics = append(out.Diagnostics, extensionDiagnosticWire(d))
	}
	for _, id := range sortedUnitIDs(eff) {
		u := eff.Units[id]
		out.Units = append(out.Units, unitSummaryWire(u, false))
		if u.Status == extpacks.UnitStatusConflict {
			out.Conflicts++
		}
	}
	out.OK = !extpacks.HasErrorDiagnostic(catalogDiags)
	s.annotatePackProvenance(r.Context(), &out)
	s.extensionsOverview.put(projectDir, out)
	return out, nil
}

// mergedDesiredWire loads one scope's merged intent and the file it came from.
func (s *Handler) mergedDesiredWire(projectDir string) (wire.ExtensionDesiredState, string, error) {
	d, _, err := extpacks.LoadMergedDesired([]string{projectDir})
	if err != nil {
		return wire.ExtensionDesiredState{}, "", err
	}
	path := ""
	if strings.TrimSpace(projectDir) != "" {
		path = extpacks.ProjectDesiredPath(projectDir)
	} else if devicePath, err := extpacks.DeviceDesiredPath(); err == nil {
		path = devicePath
	}
	note := "merged device + project desired state"
	if projectDir == "" {
		note = "device desired state only (no project_id)"
	}
	desired, err := desiredToWire(d, note)
	if err != nil {
		return wire.ExtensionDesiredState{}, "", err
	}
	return desired, path, nil
}

// extensionDiagnosticWire is the one conversion to the wire diagnostic.
func extensionDiagnosticWire(d extpacks.Diagnostic) wire.ExtensionDiagnostic {
	return wire.ExtensionDiagnostic{
		Code:          d.Code,
		Message:       d.Message,
		Severity:      wire.ExtensionDiagnosticSeverity(extpacks.SeverityForCode(d.Code)),
		UnitID:        d.UnitID,
		PackID:        d.PackID,
		MCPProviderID: d.MCPProviderID,
		ScannerID:     d.ScannerID,
	}
}

func (s *Handler) HandleGetExtensionsCatalog(w http.ResponseWriter, r *http.Request) {
	out, err := s.buildExtensionsCatalogView(r)
	if err != nil {
		requestscope.ScopeError(s.responses, w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, out)
}

func (s *Handler) HandleApplyExtensionMetaPack(w http.ResponseWriter, r *http.Request) {
	metaPackID := httpio.EncodedPathID(r, "meta_pack_id")
	if metaPackID == "" {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "meta_pack_id required")
		return
	}
	if err := extpacks.ValidatePackID(metaPackID); err != nil {
		s.responses.FailDetails(w, wire.ApiErrorCodeInvalidRequest, map[string]any{"field": "meta_pack_id"}, "invalid meta_pack_id")
		return
	}
	var req wire.ExtensionRevisionRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	if !s.requireExtensionMetaPack(w, r, metaPackID) {
		return
	}
	if _, err := s.submitExtensionIntent(r, wire.ExtensionsScopeDevice, "", req.ExpectedRevision,
		extensionstate.ApplyMetaOp{MetaPackID: metaPackID, Enable: true}); err != nil {
		s.writeMetaPackMutationError(w, r, err)
		return
	}
	view, err := s.buildExtensionsCatalogViewFresh(r)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, wire.ExtensionMutationResponse{View: view})
}

func (s *Handler) HandleUpdateExtensionMetaPack(w http.ResponseWriter, r *http.Request) {
	metaPackID := httpio.EncodedPathID(r, "meta_pack_id")
	if metaPackID == "" {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "meta_pack_id required")
		return
	}
	if err := extpacks.ValidatePackID(metaPackID); err != nil {
		s.responses.FailDetails(w, wire.ApiErrorCodeInvalidRequest, map[string]any{"field": "meta_pack_id"}, "invalid meta_pack_id")
		return
	}
	var req wire.UpdateExtensionMetaPackRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	if req.Enabled == nil {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "enabled required")
		return
	}
	if !s.requireExtensionMetaPack(w, r, metaPackID) {
		return
	}
	if _, err := s.submitExtensionIntent(r, wire.ExtensionsScopeDevice, "", req.ExpectedRevision,
		extensionstate.ApplyMetaOp{MetaPackID: metaPackID, Enable: *req.Enabled}); err != nil {
		s.writeMetaPackMutationError(w, r, err)
		return
	}
	view, err := s.buildExtensionsCatalogViewFresh(r)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, wire.ExtensionMutationResponse{View: view})
}

func (s *Handler) HandleInstallExtensionMetaPack(w http.ResponseWriter, r *http.Request) {
	var req wire.ExtensionMetaPackInstallRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	if _, err := s.submitExtensionIntent(r, wire.ExtensionsScopeDevice, "", req.ExpectedRevision,
		extensionstate.InstallMetaOp{Source: req.Source, Version: req.Version, Ref: req.Ref}); err != nil {
		s.writeMetaPackMutationError(w, r, err)
		return
	}
	view, err := s.buildExtensionsCatalogViewFresh(r)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusCreated, wire.ExtensionMutationResponse{View: view})
}

func (s *Handler) HandleDeleteExtensionMetaPack(w http.ResponseWriter, r *http.Request) {
	metaPackID := httpio.EncodedPathID(r, "meta_pack_id")
	if metaPackID == "" {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "meta_pack_id required")
		return
	}
	if err := extpacks.ValidatePackID(metaPackID); err != nil {
		s.responses.FailDetails(w, wire.ApiErrorCodeInvalidRequest, map[string]any{"field": "meta_pack_id"}, "invalid meta_pack_id")
		return
	}
	expectedRevision := strings.TrimSpace(r.URL.Query().Get("expected_revision"))
	if expectedRevision == "" {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "expected_revision required")
		return
	}
	if !s.requireExtensionMetaPack(w, r, metaPackID) {
		return
	}
	if _, err := s.submitExtensionIntent(r, wire.ExtensionsScopeDevice, "", expectedRevision,
		extensionstate.RemoveMetaOp{MetaPackID: metaPackID}); err != nil {
		s.writeMetaPackMutationError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Handler) writeMetaPackMutationError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, extpacks.ErrMetaPackNotFound) {
		s.responses.Fail(w, wire.ApiErrorCodeExtensionMetaPackNotFound, "meta-pack not found")
		return
	}
	s.writeExtensionMutationError(w, r, err)
}
