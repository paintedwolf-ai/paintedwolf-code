package editordoc

import (
	"context"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/sourcebranch"
)

func (s *Service) projectBranch(ctx context.Context, projectID string, branch sourcebranch.ID) (*project.Project, error) {
	if s.roots == nil {
		return nil, ErrNotFound
	}
	p, err := s.roots.Get(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return project.ResolveBranch(ctx, s.store.db, p, branch)
}

func checkWorkspace(p *project.Project, d *Document) error {
	if p == nil || p.ID != d.ProjectID || p.BranchForRoot(d.RootID) != d.BranchID {
		return ErrNotFound
	}
	return nil
}

// Workspace resolves an addressed document independently of the focused chat.
func (s *Service) Workspace(ctx context.Context, p *project.Project, id string) (*project.Project, error) {
	d, err := s.checked(ctx, id, p.ID)
	if err != nil {
		return nil, err
	}
	if p.BranchForRoot(d.RootID) == d.BranchID {
		return p, nil
	}
	return s.projectBranch(ctx, p.ID, d.BranchID)
}

func (s *Service) projectWorkspacePresentation(ctx context.Context, d *Document) {
	if p, err := s.projectBranch(ctx, d.ProjectID, d.BranchID); err == nil {
		d.WorkspaceID = p.WorkspaceID()
	}
}
