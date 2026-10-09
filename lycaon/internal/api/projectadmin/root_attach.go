package projectadmin

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectcontrib"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Roots) HandleAttachProjectRoot(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(chi.URLParam(r, "id"))
	var req wire.AttachProjectRootRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	if strings.TrimSpace(req.Path) == "" {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "path is required")
		return
	}
	if _, ok := s.Projects.requireProject(w, r, id); !ok {
		return
	}
	release := s.Projects.beginProjectMutation(w, r, id)
	if release == nil {
		return
	}
	defer release()
	if !s.ensureProjectRootsMutable(w, r, id) {
		return
	}
	change, err := s.Registry.AttachRoot(r.Context(), id, project.AttachRootParams{
		Path:      req.Path,
		Label:     req.Label,
		IsPrimary: req.IsPrimary,
	})
	if err != nil {
		s.responses.ProjectRegistryError(w, r, err)
		return
	}
	if change.Added != nil {
		s.sourceViews.InvalidateProjectSourceViews(id)
		s.afterRootAttached(r.Context(), id, change.Added.Path)
	}
	s.Verification.detectVerifyAsync(r.Context(), id)
	s.Sessions.EnqueueRootsChangedKick(r.Context(), id, project.RootRefsFrom(change.Before), project.RootRefsFrom(change.After))
	s.Sessions.ReopenBoardOrientationOnRootAttach(r.Context(), id)
	s.Projects.publishProjectLifecycleEvent(r.Context(), wire.ProjectEventUpdated, change.After)
	httpio.WriteJSON(w, http.StatusCreated, project.ToAPI(change.After))
}

func (s *Roots) HandleUpdateProjectRoot(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(chi.URLParam(r, "id"))
	rootID := strings.TrimSpace(chi.URLParam(r, "root_id"))
	var req wire.UpdateProjectRootRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	if _, ok := s.Projects.requireProject(w, r, id); !ok {
		return
	}
	release := s.Projects.beginProjectMutation(w, r, id)
	if release == nil {
		return
	}
	defer release()
	if !s.ensureProjectRootsMutable(w, r, id) {
		return
	}
	change, err := s.Registry.PatchRoot(r.Context(), id, rootID, project.PatchRootParams{
		IsPrimary: req.IsPrimary,
		Label:     req.Label,
	})
	if err != nil {
		s.responses.ProjectRegistryError(w, r, err)
		return
	}
	s.sourceViews.InvalidateProjectSourceViews(id)
	s.Sessions.InvalidateSessionWorkspacePaths(r.Context(), id)
	if change.RootContextChanged {
		s.Sessions.EnqueueRootsChangedKick(r.Context(), id, project.RootRefsFrom(change.Before), project.RootRefsFrom(change.After))
	}
	if change.RootContextChanged {
		s.sourceWatch.ScheduleSourceInventory(r.Context(), id)
	}
	s.Projects.publishProjectLifecycleEvent(r.Context(), wire.ProjectEventUpdated, change.After)
	httpio.WriteJSON(w, http.StatusOK, project.ToAPI(change.After))
}

func (s *Roots) ensureProjectRootsMutable(w http.ResponseWriter, r *http.Request, projectID string) bool {
	if s.Sessions == nil {
		return true
	}
	dependents, err := s.Sessions.ProjectDependents(r.Context(), projectID)
	if err != nil {
		s.responses.ProjectRegistryError(w, r, err)
		return false
	}
	if dependents.HasAny() {
		s.Projects.writeRootBusy(w, dependents)
		return false
	}
	return true
}

func (s *Roots) afterRootAttached(ctx context.Context, projectID, workspacePath string) {
	s.ScanCadence.RootAttached(workspacePath)
	var overlayPaths []string
	var p *project.Project
	if loaded, err := s.Registry.Get(ctx, projectID); err == nil && loaded != nil {
		p = loaded
		if overlay, overlayErr := project.ResolveProjectOverlay(p, ""); overlayErr == nil {
			overlayPaths = overlay.Paths
		}
	}
	if len(overlayPaths) == 0 && strings.TrimSpace(workspacePath) != "" {
		overlayPaths = []string{workspacePath}
	}
	for _, rootPath := range overlayPaths {
		if err := s.Sessions.WarmPostureOverlayForProject(p, rootPath); err != nil {
			slog.WarnContext(ctx, "warm posture overlay on root attach", "path", rootPath, "err", err)
		}
	}
	s.Sessions.Catalog().InvalidateEffectiveCatalog(projectID)
	if s.ProjectRules != nil && len(overlayPaths) > 0 && requestscope.ProjectSurfaceApplies(s.Settings, p, projectcontrib.SurfaceProjectSettings) {
		if err := s.ProjectRules.WarmOverlays(overlayPaths); err != nil {
			slog.WarnContext(ctx, "warm project rules overlay on root attach", "paths", overlayPaths, "err", err)
		}
	}
	s.Git.WarmRepoBrief(workspacePath)
	s.sourceWatch.ScheduleSourceInventory(ctx, projectID)
	s.background.Go(ctx, func(ctx context.Context) {
		s.attachRootBackgroundWarm(ctx, projectID, workspacePath)
	})
}

// attachRootBackgroundWarm performs whole-tree work outside the request.
func (s *Roots) attachRootBackgroundWarm(ctx context.Context, projectID, workspacePath string) {
	s.sourceWatch.ScheduleSourceWatch(ctx, projectID)
	if removed := s.Sandboxes.reconcileProjectSandboxes(ctx, projectID, workspacePath); removed > 0 {
		slog.InfoContext(ctx, "reconciled stale worker sandboxes on root attach", "project_id", projectID, "workspace_path", workspacePath, "removed", removed)
	}
	if err := s.sourceWatch.AwaitAttachedRootStructure(ctx, projectID, workspacePath); err != nil {
		slog.WarnContext(ctx, "source structure before background warm-up", "path", workspacePath, "err", err)
		return
	}
	s.Git.WarmStatus(s.background, ctx, workspacePath)
	if err := s.baselineSecurity(ctx, workspacePath); err != nil {
		slog.WarnContext(ctx, "security baseline on root attach", "path", workspacePath, "err", err)
	}
}

// baselineSecurity records the attached root's generation as each scanner's
// base. Nothing is scanned: automatic scanning covers what changes from here.
func (s *Roots) baselineSecurity(ctx context.Context, projectDir string) error {
	if s.ScanCadence == nil {
		return nil
	}
	return s.ScanCadence.BaselineRoot(ctx, projectDir)
}
