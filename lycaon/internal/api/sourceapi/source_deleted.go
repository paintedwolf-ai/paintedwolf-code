package sourceapi

import (
	"context"
	"errors"

	"github.com/lycaon/lycaon/internal/api/secretview"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/workspace"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Handler) ResolveNavigationPaths(ctx context.Context, p *project.Project, branch sourcebranch.ID, refs []wire.NavigationReference) []wire.NavigationReference {
	out := project.ResolveNavigation(ctx, p, refs)
	for i := range out {
		ref := &out[i]
		ref.Deleted = false
		if ref.Status != wire.NavigationMissing {
			continue
		}
		path, err := project.ResolveAbsentSourcePath(p, ref.RootID, ref.Path)
		if err != nil {
			ref.Status = wire.NavigationUnavailable
			continue
		}
		_, err = s.SourceLedger.ResolveDeletedPath(ctx, p.ID, branch, ref.RootID, path)
		switch {
		case err == nil:
			ref.Status, ref.EntryKind, ref.Deleted = wire.NavigationResolved, wire.NavigationEntryKind("file"), true
		case !errors.Is(err, sourceledger.ErrHistoryNotFound):
			ref.Status = wire.NavigationUnavailable
		}
	}
	return out
}

// readDeletedSource serves retained content only while the exact address is absent.
func (s *Handler) readDeletedSource(ctx context.Context, p *project.Project, req project.SourceReadRequest, scope sourceViewerWorkspace) (*wire.ProjectSourceReadResponse, error) {
	path, err := project.ResolveAbsentSourcePath(p, req.RootID, req.Path)
	if err != nil {
		return nil, err
	}
	deleted, err := s.SourceLedger.ResolveDeletedPath(ctx, p.ID, scope.branch, req.RootID, path)
	if errors.Is(err, sourceledger.ErrHistoryNotFound) {
		return nil, project.ErrSourceNotFound
	}
	if err != nil {
		return nil, err
	}
	previous, err := s.SourceLedger.DeletedPathContent(ctx, p.ID, deleted)
	if err != nil {
		return nil, err
	}
	currentPath, err := project.ResolveAbsentSourcePath(p, req.RootID, req.Path)
	if err != nil {
		return nil, err
	}
	if currentPath != path {
		return nil, project.ErrSourceNotFound
	}
	side := mapSourceComparisonSide(previous)
	if side.Availability == "available" {
		if screenCtx, _, err := secretview.ProjectContext(s.ManagedSecrets, ctx, p.ID); err == nil {
			side.SecretScreen = secretview.ScreenText(s.SecretSpans, screenCtx, side.Content)
		}
	}
	var source *wire.VersionComparisonSource
	if previous.VersionID != "" {
		source = &wire.VersionComparisonSource{Kind: "version", VersionID: deleted.VersionID}
	}
	return &wire.ProjectSourceReadResponse{
		FileID: deleted.FileID, VersionID: deleted.VersionID, RootID: req.RootID, Path: path,
		WorkspaceID: scope.id, WorkspaceKind: scope.kind,
		Deleted: &wire.SourceDeletedFile{DeletedAt: deleted.DeletedTS, Previous: *readerEndpoint(side), Source: source},
	}, nil
}

func SourceProjectInBranch(p *project.Project, branchRoot string) (*project.Project, error) {
	if branchRoot == "" {
		return p, nil
	}
	roots, err := workspace.BranchRootRefs(branchRoot)
	if err != nil {
		return nil, err
	}
	attached := make(map[string]bool, len(p.Roots))
	for _, root := range p.Roots {
		attached[root.ID] = true
	}
	scoped := *p
	scoped.Roots = nil
	for _, root := range roots {
		if attached[root.ID] {
			scoped.Roots = append(scoped.Roots, project.Root{ID: root.ID, Path: root.Path, Label: root.Label, IsPrimary: root.IsPrimary})
		}
	}
	return &scoped, nil
}
