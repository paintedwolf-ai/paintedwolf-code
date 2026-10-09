package extensionadmin

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/lycaon/lycaon/internal/api/projectview"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/extensionstate"
	"github.com/lycaon/lycaon/internal/extpacks"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// extensionPublisher propagates committed generations to session consumers.
type extensionPublisher struct{ s *Mutations }

func (p extensionPublisher) InvalidateProjects(ctx context.Context, projectID string) {
	p.s.InvalidateEffectiveCatalog(ctx, projectID)
	if strings.TrimSpace(projectID) == "" {
		// Device mutations can change active detections.
		p.s.Scan.ReloadDetectionPacks(ctx)
	}
}

// extensionEmitter publishes committed extension settings changes.
type extensionEmitter struct{ s *Mutations }

func (e extensionEmitter) ExtensionsChanged(ctx context.Context, scope extensionstate.Scope) {
	settingsScope := string(wire.SettingsScopeGlobal)
	if scope.Kind == "project" {
		settingsScope = string(wire.SettingsScopeProject)
	}
	projectview.PublishSettings(e.s.Events, e.s.Projects, ctx, wire.SettingsAreaExtensions, settingsScope, scope.ProjectDir, "updated")
}

// submitExtensionIntent runs one mutation through the subsystem owner.
func (s *Mutations) submitExtensionIntent(r *http.Request, scope wire.ExtensionsDesiredScope, projectDir, expectedRevision string, op extensionstate.Op) (extensionstate.Result, error) {
	return s.Owner.Apply(r.Context(), extensionstate.Intent{
		Scope: extensionstate.Scope{
			Kind:       string(scope),
			ProjectID:  strings.TrimSpace(r.URL.Query().Get("project_id")),
			ProjectDir: projectDir,
		},
		ExpectedRevision: expectedRevision,
		Op:               op,
	})
}

// writeExtensionMutationError maps typed subsystem-owner results onto the wire.
func (s *Mutations) writeExtensionMutationError(w http.ResponseWriter, r *http.Request, err error) {
	var rejected *extensionstate.RejectedError
	switch {
	case errors.Is(err, extensionstate.ErrExpectedRevisionRequired):
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "expected_revision required")
	case isStale(err):
		s.responses.Fail(w, wire.ApiErrorCodeExtensionStateChanged,
			"extension state changed since the expected revision; reload and retry")
	case errors.Is(err, extensionstate.ErrProjectMutationUnsupported):
		s.responses.FailReason(w, wire.ApiErrorCodeInvalidRequest, "project extension state may only disable units")
	case errors.Is(err, extensionstate.ErrProjectUnitDisableUnsupported):
		s.responses.FailReason(w, wire.ApiErrorCodeInvalidRequest, "this unit cannot be disabled for one project")
	case errors.Is(err, extensionstate.ErrUnknownUnit):
		s.responses.Fail(w, wire.ApiErrorCodeExtensionUnitNotFound, "extension unit not found")
	case errors.Is(err, extensionstate.ErrUnknownPack), errors.Is(err, extpacks.ErrPackNotInstalled):
		s.responses.Fail(w, wire.ApiErrorCodeExtensionPackNotFound, "extension pack not installed")
	case errors.Is(err, extpacks.ErrStockPack), errors.Is(err, extpacks.ErrStockMetaPack):
		s.responses.Fail(w, wire.ApiErrorCodeStockPackImmutable, "stock packs cannot be changed here")
	case errors.Is(err, extpacks.ErrLinkedSourceMissing):
		s.responses.Fail(w, wire.ApiErrorCodePathMissing, "the linked pack folder is missing")
	case errors.As(err, &rejected):
		s.responses.Logger.InfoContext(r.Context(), "extension candidate rejected", "err", rejected)
		s.responses.FailReason(w, wire.ApiErrorCodeExtensionCandidateRejected, "The extension candidate could not be prepared or validated. Check its source and configuration.")
	default:
		s.responses.InternalError(w, r, err)
	}
}

func isStale(err error) bool {
	var stale *extensionstate.StaleError
	return errors.As(err, &stale)
}

// currentExtensionRevision reads the optimistic token for GET responses.
func (s *Mutations) currentExtensionRevision(projectDir string) (string, error) {
	return s.Owner.CurrentRevision(projectDir)
}

func (s *Mutations) InvalidateEffectiveCatalog(ctx context.Context, projectID string) {
	s.Sessions.Catalog().InvalidateEffectiveCatalog(projectID)
	s.Contributions.invalidateDeviceContributionFrame()
	// Complete trust invalidation after client disconnects.
	detached := context.WithoutCancel(ctx)
	s.WarmEffectiveCatalog(detached, projectID)
	s.background.Go(detached, func(ctx context.Context) {
		_ = s.Contributions.WarmContributionFrame(ctx)
	})
	s.dropProjectMCPSessions(detached, projectID)
}

// WarmEffectiveCatalog prepares the project catalog and committed view.
func (s *Mutations) WarmEffectiveCatalog(ctx context.Context, projectID string) {
	if strings.TrimSpace(projectID) == "" {
		return
	}
	s.background.Go(ctx, func(ctx context.Context) {
		// Contribution reads require the committed view.
		s.Sessions.Catalog().ViewForProject(ctx, projectID)
	})
}

// dropProjectMCPSessions reapplies changed trust to running MCP processes.
func (s *Mutations) dropProjectMCPSessions(ctx context.Context, projectID string) {
	if strings.TrimSpace(projectID) == "" {
		return
	}
	slog.DebugContext(ctx, "dropping mcp sessions after trust change", "project_id", projectID)
	s.MCP.CloseProjectSessions(projectID)
}

// The require* checks resolve the resource a mutation addresses before the
// owner compares revisions, so an unknown id answers not found whatever
// revision the request carries.

func (s *Mutations) requireExtensionPack(w http.ResponseWriter, r *http.Request, packID string) bool {
	eff, _, err := s.resolveExtensionsCatalog(r)
	if err != nil {
		requestscope.ScopeError(s.responses, w, r, err)
		return false
	}
	for _, p := range eff.Packs {
		if p.ID == packID {
			return true
		}
	}
	s.responses.Fail(w, wire.ApiErrorCodeExtensionPackNotFound, "extension pack not installed")
	return false
}

func (s *Mutations) requireExtensionProfile(w http.ResponseWriter, r *http.Request, packID, profileName string) bool {
	if !s.requireExtensionPack(w, r, packID) {
		return false
	}
	if _, err := extpacks.LoadDeviceProfile(packID, profileName); errors.Is(err, extpacks.ErrProfileNotFound) {
		s.responses.Fail(w, wire.ApiErrorCodeExtensionProfileNotFound, "extension profile not found")
		return false
	}
	return true
}

func (s *Mutations) requireExtensionUnit(w http.ResponseWriter, r *http.Request, unitID string) bool {
	eff, _, err := s.resolveExtensionsCatalog(r)
	if err != nil {
		requestscope.ScopeError(s.responses, w, r, err)
		return false
	}
	if _, ok := eff.Units[unitID]; !ok {
		s.responses.Fail(w, wire.ApiErrorCodeExtensionUnitNotFound, "extension unit not found")
		return false
	}
	return true
}

func (s *Mutations) requireExtensionMetaPack(w http.ResponseWriter, r *http.Request, metaPackID string) bool {
	metas, _, err := extpacks.DiscoverMetaPacks()
	if err != nil {
		s.responses.InternalError(w, r, err)
		return false
	}
	for _, m := range metas {
		if m.Manifest.ID == metaPackID {
			return true
		}
	}
	s.responses.Fail(w, wire.ApiErrorCodeExtensionMetaPackNotFound, "meta-pack not found")
	return false
}
