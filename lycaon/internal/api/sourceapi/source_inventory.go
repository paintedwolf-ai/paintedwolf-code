package sourceapi

import (
	"context"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/sourceledger"
)

// InventoryService tracks workspace inventory and drift; the source ledger implements it.
type InventoryService interface {
	EnsureInventory(context.Context, sourceledger.InventoryRequest) error
	SuspendInventory(context.Context, string) (func(), error)
	InventoryState(context.Context, string, sourcebranch.ID, int) (sourceledger.InventoryState, error)
	// ObservePaths records drift for named paths without a pass.
	ObservePaths(context.Context, string, []sourceledger.RootSpec, []sourceledger.PathRef) (int, error)
}

func sourceInventoryRequest(p *project.Project) sourceledger.InventoryRequest {
	if p == nil {
		return sourceledger.InventoryRequest{}
	}
	roots := make([]sourceledger.RootSpec, 0, len(p.Roots))
	for _, root := range p.Roots {
		roots = append(roots, sourceledger.RootSpec{ID: root.ID, BranchID: p.BranchForRoot(root.ID), Path: root.Path})
	}
	return sourceledger.InventoryRequest{
		ProjectID: p.ID, RootsGeneration: p.RootsGeneration, Roots: roots,
	}
}

// ScheduleSourceInventory starts a coalesced background inventory.
func (s *Watch) ScheduleSourceInventory(parent context.Context, projectID string) {
	if projectID == "" {
		return
	}
	p, err := s.ProjectRegistry.Get(parent, projectID)
	if err != nil || p == nil {
		return
	}
	s.scheduleWorkspaceInventory(parent, p)
}

func (s *Watch) AwaitAttachedRootStructure(ctx context.Context, projectID, rootPath string) error {
	p, err := s.ProjectRegistry.Get(ctx, projectID)
	if err != nil {
		return err
	}
	for _, root := range p.Roots {
		if root.Path == rootPath {
			return sourcecatalog.Process().Directories.AwaitNavigation(ctx, p.ID, sourcecatalog.Root{ID: root.ID, Path: root.Path})
		}
	}
	return nil
}

func (s *Watch) scheduleWorkspaceInventory(parent context.Context, p *project.Project) {
	if p == nil {
		return
	}
	inventory := s.SourceInventory
	req := sourceInventoryRequest(p)
	s.background.Go(parent, func(ctx context.Context) {
		for _, root := range p.Roots {
			if err := sourcecatalog.Process().Directories.WarmNavigation(ctx, p.ID, sourcecatalog.Root{ID: root.ID, Path: root.Path}); err != nil {
				s.responses.Logger.WarnContext(ctx, "source structure", "project_id", p.ID, "root", root.ID, "err", err)
			}
		}
		if inventory == nil {
			return
		}
		for _, root := range p.Roots {
			if err := sourcecatalog.Process().Directories.AwaitNavigation(ctx, p.ID, sourcecatalog.Root{ID: root.ID, Path: root.Path}); err != nil {
				s.responses.Logger.WarnContext(ctx, "source structure", "project_id", p.ID, "root", root.ID, "err", err)
				return
			}
		}
		if err := inventory.EnsureInventory(ctx, req); err != nil {
			s.responses.Logger.WarnContext(ctx, "source inventory", "project_id", req.ProjectID,
				"roots_generation", req.RootsGeneration, "err", err)
			return
		}
		s.noteScanCadenceInventory(ctx, p)
	})
}

func (s *Watch) noteScanCadenceInventory(ctx context.Context, p *project.Project) {
	if s == nil || s.ScanCadence == nil || p == nil {
		return
	}
	for _, root := range p.Roots {
		files, err := sourcecatalog.Process().Trees.RootFileCount(ctx, p.ID,
			sourcecatalog.Root{ID: root.ID, Path: root.Path}, sourcecatalog.FileScope{Audience: sourcecatalog.AgentAudience, IncludeHidden: true}, 0)
		if err != nil {
			s.responses.Logger.WarnContext(ctx, "scan cadence root file count", "path", root.Path, "err", err)
			continue
		}
		if !files.Measured {
			// Partial counts cannot establish a drift baseline.
			continue
		}
		if err := s.ScanCadence.NoteTree(ctx, root.Path, files.Count); err != nil {
			s.responses.Logger.WarnContext(ctx, "scan cadence note tree", "path", root.Path, "err", err)
		}
	}
}

func (s *Watch) sourceInventoryState(
	ctx context.Context,
	p *project.Project,
) (sourceledger.InventoryState, error) {
	if p == nil || s.SourceInventory == nil {
		return sourceledger.InventoryState{Phase: sourceledger.InventoryUninitialized}, nil
	}
	return s.SourceInventory.InventoryState(ctx, p.ID, p.SourceBranch, p.RootsGeneration)
}
