package projectadmin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/project"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Promotion) HandleCancelProjectPromotion(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(chi.URLParam(r, "id"))
	if _, ok := s.Projects.requireProject(w, r, id); !ok {
		return
	}
	release := s.Projects.beginProjectMutation(w, r, id)
	if release == nil {
		return
	}
	defer release()
	p, err := s.PromotionEngine().Cancel(r.Context(), id)
	if err != nil {
		s.responses.ProjectRegistryError(w, r, err)
		return
	}
	s.Projects.publishProjectLifecycleEvent(r.Context(), wire.ProjectEventUpdated, p)
	w.WriteHeader(http.StatusNoContent)
}

// TryRunPromotion resumes a durable save-to-folder when the project is quiescent.
func (s *Promotion) TryRunPromotion(ctx context.Context, projectID string) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return
	}
	p, err := s.Registry.Get(ctx, projectID)
	if err != nil || p == nil || p.Promotion == nil {
		return
	}
	quiescent, err := s.Sessions.ProjectControl.ProjectPromoteQuiescent(ctx, projectID)
	if err != nil || !quiescent {
		return
	}
	promoted, execErr := s.executeDraftPromote(ctx, projectID, p.Promotion.DestinationPath, p.Promotion.InitGit)
	if execErr != nil {
		if current, loadErr := s.Registry.Get(ctx, projectID); loadErr == nil {
			s.Projects.publishProjectLifecycleEvent(ctx, wire.ProjectEventUpdated, current)
		}
		return
	}
	s.Projects.publishProjectLifecycleEvent(ctx, wire.ProjectEventUpdated, promoted)
}

func (s *Promotion) executeDraftPromote(ctx context.Context, id, destPath string, initGit bool) (*project.Project, error) {
	if gateErr := s.MutationGate.BeginMutation(id); gateErr != nil {
		return nil, gateErr
	}
	defer s.MutationGate.EndMutation(id)
	quiescent, err := s.Sessions.ProjectControl.ProjectPromoteQuiescent(ctx, id)
	if err != nil {
		return nil, err
	}
	if !quiescent {
		return nil, project.ErrProjectBusy
	}
	before, err := s.Registry.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	engine := s.PromotionEngine()
	if before.Promotion == nil {
		if _, err := engine.Create(ctx, id, destPath, initGit); err != nil {
			return nil, err
		}
	} else if !project.SamePath(before.Promotion.DestinationPath, destPath) || before.Promotion.InitGit != initGit {
		return nil, project.ErrPromotionConflict
	}
	p, err := engine.Run(ctx, id)
	if err != nil {
		return nil, err
	}
	if s.sourceViews != nil {
		s.sourceViews.InvalidateProjectSourceViews(id)
	}
	folder := project.PrimaryRootPath(p)
	s.Roots.afterRootAttached(ctx, id, folder)
	s.Verification.detectVerifyAsync(ctx, id)
	s.Sessions.Chats.ReopenOrientation(ctx, id, s.Roots.Sandboxes.Board)
	return p, nil
}

func (s *Promotion) PromotionEngine() *project.PromotionEngine {
	return project.NewPromotionEngine(s.Registry, func(ctx context.Context, path string) error {
		return s.Git.Manager().Init(ctx, path)
	})
}

// RecoverPromotions resumes every durable promotion intent during boot.
func (s *Promotion) RecoverPromotions(ctx context.Context) error {
	promotions, err := s.Registry.ListPromotions(ctx)
	if err != nil {
		return err
	}
	var recoveryErr error
	for _, promotion := range promotions {
		p, runErr := s.executeDraftPromote(ctx, promotion.ProjectID, promotion.DestinationPath, promotion.InitGit)
		if runErr != nil {
			recoveryErr = errors.Join(recoveryErr, fmt.Errorf("project %s: %w", promotion.ProjectID, runErr))
			continue
		}
		s.Projects.publishProjectLifecycleEvent(ctx, wire.ProjectEventUpdated, p)
	}
	return recoveryErr
}
