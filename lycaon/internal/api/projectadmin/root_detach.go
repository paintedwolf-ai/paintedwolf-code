package projectadmin

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/sourcefeed"
	"github.com/lycaon/lycaon/internal/workspace"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Handler) HandleDetachProjectRoot(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(chi.URLParam(r, "id"))
	rootID := strings.TrimSpace(chi.URLParam(r, "root_id"))
	force, ok := s.queryForce(w, r)
	if !ok {
		return
	}
	if _, ok := s.requireProject(w, r, id); !ok {
		return
	}
	release, waitForDrain := s.beginDestructiveProjectMutation(w, r, id)
	if release == nil {
		return
	}
	defer release()
	before, err := s.Registry.Get(r.Context(), id)
	if err != nil {
		s.responses.ProjectLookupError(w, r, err)
		return
	}
	detachedPath := ""
	for _, root := range before.Roots {
		if root.ID == rootID {
			detachedPath = root.Path
			break
		}
	}
	dependents, err := s.Sessions.RootDependents(r.Context(), id, rootID)
	if err != nil {
		s.responses.ProjectRegistryError(w, r, err)
		return
	}
	dependents, err = s.withEditorDocumentDependents(r.Context(), dependents, id, rootID)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	if dependents.HasAny() && !force {
		s.writeRootBusy(w, dependents)
		return
	}
	if force && dependents.HasAny() {
		if err := s.Sessions.ForceCancelForRootDetach(r.Context(), id, rootID, dependents, detachedPath); err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
	}
	if force {
		if err := waitForDrain(r.Context()); err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
	} else if !s.MutationGate.MutationDrained(id) {
		s.responses.Fail(w, wire.ApiErrorCodeProjectBusy, "project has in-flight work")
		return
	}
	mutationCtx := r.Context()
	if force {
		mutationCtx = project.WithForcedLifecycle(mutationCtx)
	}
	change, err := s.Registry.DetachRoot(mutationCtx, id, rootID)
	if err != nil {
		s.responses.ProjectRegistryError(w, r, err)
		return
	}
	s.forgetRemovedEditorDocuments(dependents)
	s.Sources.InvalidateProjectSourceViews(id)
	sourcefeed.StopProjectWatch(r.Context(), id)
	afterRoots := project.RootRefsFrom(change.After)
	s.Sessions.ReassignSessionsAfterRootDetach(r.Context(), id, rootID, afterRoots)
	s.Sessions.InvalidateSessionWorkspacePaths(r.Context(), id)
	s.Sessions.EnqueueRootsChangedKick(r.Context(), id, project.RootRefsFrom(change.Before), afterRoots)
	if detachedPath != "" {
		s.releaseUnattachedSourceRoots(r.Context(), []string{detachedPath})
		if removed := s.reconcileProjectSandboxes(r.Context(), id, detachedPath); removed > 0 {
			slog.InfoContext(r.Context(), "reconciled stale worker sandboxes on root detach", "project_id", id, "workspace_path", detachedPath, "removed", removed)
		}
		s.Settings.Verify.ClearProposal(detachedPath)
		if err := workspace.RemoveSeed(r.Context(), s.WorkerSeedRoot, detachedPath); err != nil {
			slog.WarnContext(r.Context(), "remove worker seed on root detach", "path", detachedPath, "err", err)
		}
		// Checkpoints are keyed by source root, and a detached root is one no
		// session can rewind into any more.
		if err := s.removeRootCheckpoints(r.Context(), id, detachedPath); err != nil {
			slog.WarnContext(r.Context(), "remove session checkpoints on root detach", "path", detachedPath, "err", err)
		}
	}
	if len(afterRoots) > 0 {
		s.detectVerifyAsync(r.Context(), id)
	}
	s.Sources.ScheduleSourceInventory(r.Context(), id)
	s.publishProjectLifecycleEvent(r.Context(), wire.ProjectEventUpdated, change.After)
	w.WriteHeader(http.StatusNoContent)
}

//nolint:contextcheck // Inventory resumes under the server lifetime after mutation cleanup.
func (s *Handler) beginDestructiveProjectMutation(w http.ResponseWriter, r *http.Request, projectID string) (func(), func(context.Context) error) {
	release, drain := s.beginDrainingProjectMutation(w, r, projectID)
	if release == nil || s.Sources.SourceInventory == nil {
		return release, drain
	}
	resume, err := s.Sources.SourceInventory.SuspendInventory(r.Context(), projectID)
	finish := func() {
		resume()
		release()
		// A surviving project resumes with its current root composition.
		s.Sources.ScheduleSourceInventory(s.background.Context(), projectID)
	}
	if err != nil {
		finish()
		s.responses.InternalError(w, r, err)
		return nil, nil
	}
	return finish, drain
}

func (s *Handler) withEditorDocumentDependents(ctx context.Context, dependents session.RootDependents, projectID, rootID string) (session.RootDependents, error) {
	if s.Sources.EditorDocuments == nil {
		return dependents, nil
	}
	documents, err := s.Sources.EditorDocuments.LifecycleDependents(ctx, projectID, rootID)
	if err != nil {
		return session.RootDependents{}, err
	}
	for _, document := range documents {
		dependents.Documents = append(dependents.Documents, session.RootDependentDocument{
			DocumentID: document.ID,
			Path:       document.Path,
		})
	}
	return dependents, nil
}

func (s *Handler) forgetRemovedEditorDocuments(dependents session.RootDependents) {
	if s.Sources.EditorDocuments == nil || len(dependents.Documents) == 0 {
		return
	}
	ids := make([]string, 0, len(dependents.Documents))
	for _, document := range dependents.Documents {
		ids = append(ids, document.DocumentID)
	}
	s.Sources.EditorDocuments.ForgetRemoved(ids)
}
