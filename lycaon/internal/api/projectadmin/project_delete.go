package projectadmin

import (
	"context"
	"log/slog"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectremoval"
	"github.com/lycaon/lycaon/internal/session/projectcontrol"
	"github.com/lycaon/lycaon/internal/sourcefeed"
	"github.com/lycaon/lycaon/internal/workspace"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Handler) deleteProject(ctx context.Context, id string, force bool) error {
	release, waitForDrain, err := s.acquireProjectDeletion(ctx, id)
	if err != nil {
		return err
	}
	defer release()
	p, err := s.Registry.Get(ctx, id)
	if err != nil {
		return err
	}
	removedRootPaths := rootPathsOf(p)
	dependents, err := s.Sessions.ProjectControl.ProjectDependents(ctx, id)
	if err != nil {
		return err
	}
	dependents, err = s.withEditorDocumentDependents(ctx, dependents, id, "")
	if err != nil {
		return err
	}
	if dependents.HasAny() && !force {
		return &projectremoval.Failure{Code: wire.ApiErrorCodeRootBusy, Details: dependents.Details(), Message: "The project has in-flight dependents.", Documents: len(dependents.Documents), Sessions: len(dependents.Sessions), Workers: len(dependents.Workers), Overlays: len(dependents.Overlays)}
	}
	if force && dependents.HasAny() {
		if err := s.Sessions.ProjectControl.ForceCancelForProjectDelete(ctx, id, dependents); err != nil {
			return err
		}
	}
	if force {
		if err := waitForDrain(ctx); err != nil {
			return err
		}
	} else if !s.MutationGate.MutationDrained(id) {
		return project.ErrProjectBusy
	}
	if err := s.Sessions.Chats.RetireForProjectDelete(ctx, id); err != nil {
		return err
	}
	mutationCtx := ctx
	if force {
		mutationCtx = project.WithForcedLifecycle(mutationCtx)
	}
	removeSecretValues, err := s.ManagedSecrets.ProjectRemoval(ctx, id)
	if err != nil {
		return err
	}
	if err := s.Registry.Delete(mutationCtx, id); err != nil {
		return err
	}
	if removeSecretValues != nil {
		if err := removeSecretValues(); err != nil {
			slog.WarnContext(ctx, "remove managed secrets after project delete", "project_id", id, "err", err)
		}
	}
	s.finishProjectDeletion(ctx, p, dependents, removedRootPaths)
	return nil
}

func (s *Handler) finishProjectDeletion(ctx context.Context, p *project.Project, dependents projectcontrol.RootDependents, removedRootPaths []string) {
	id := p.ID
	s.forgetRemovedEditorDocuments(dependents)
	s.Sources.InvalidateProjectSourceViews(id)
	s.releaseUnattachedSourceRoots(ctx, removedRootPaths)
	sourcefeed.StopProjectWatch(ctx, id)
	// Project deletion removes only engine-managed storage.
	if err := project.RemoveHostDataDir(s.DataDir, id); err != nil {
		slog.WarnContext(ctx, "remove project host data", "project_id", id, "err", err)
	}
	if err := project.RemoveDraftScratch(s.DataDir, id); err != nil {
		slog.WarnContext(ctx, "remove project draft scratch", "project_id", id, "err", err)
	}
	for _, root := range p.Roots {
		if err := workspace.RemoveSeed(ctx, s.WorkerSeedRoot, root.Path); err != nil {
			slog.WarnContext(ctx, "remove worker seed on project delete", "path", root.Path, "err", err)
		}
		if err := s.removeRootCheckpoints(ctx, id, root.Path); err != nil {
			slog.WarnContext(ctx, "remove session checkpoints on project delete", "path", root.Path, "err", err)
		}
	}
	s.publishProjectLifecycleEvent(ctx, wire.ProjectEventDeleted, &project.Project{ID: id})
}

func (s *Handler) acquireProjectDeletion(ctx context.Context, id string) (func(), func(context.Context) error, error) {
	drain, err := s.MutationGate.BeginDrainingMutation(id)
	if err != nil {
		return nil, nil, err
	}
	release := func() { s.MutationGate.EndMutation(id) }
	if s.Sources.SourceInventory == nil {
		return release, drain, nil
	}
	resume, err := s.Sources.SourceInventory.SuspendInventory(ctx, id)
	finish := func() {
		resume()
		release()
		s.Sources.ScheduleSourceInventory(context.WithoutCancel(ctx), id)
	}
	if err != nil {
		finish()
		return nil, nil, err
	}
	return finish, drain, nil
}
