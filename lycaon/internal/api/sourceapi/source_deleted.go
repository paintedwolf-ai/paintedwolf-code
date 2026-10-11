package sourceapi

import (
	"context"
	"errors"

	"github.com/lycaon/lycaon/internal/api/secretview"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectsource"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourceledger"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Workspace) ResolveNavigationPaths(ctx context.Context, p *project.Project, branch sourcebranch.ID, refs []wire.NavigationReference) []wire.NavigationReference {
	out := project.ResolveNavigation(ctx, p, refs)
	for i := range out {
		ref := &out[i]
		ref.Deleted = false
		if ref.Status != wire.NavigationMissing {
			continue
		}
		path, err := projectsource.ResolveAbsentSourcePath(p, ref.RootID, ref.Path)
		if err != nil {
			ref.Status = wire.NavigationUnavailable
			continue
		}
		_, err = s.SourceLedger.History.ResolveDeletedPath(ctx, p.ID, branch, ref.RootID, path)
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
func (s *Workspace) readDeletedSource(ctx context.Context, p *project.Project, req projectsource.SourceReadRequest, scope sourceViewerWorkspace) (*wire.ProjectSourceReadResponse, error) {
	path, err := projectsource.ResolveAbsentSourcePath(p, req.RootID, req.Path)
	if err != nil {
		return nil, err
	}
	deleted, err := s.SourceLedger.History.ResolveDeletedPath(ctx, p.ID, scope.branch, req.RootID, path)
	if errors.Is(err, sourceledger.ErrHistoryNotFound) {
		return nil, projectsource.ErrSourceNotFound
	}
	if err != nil {
		return nil, err
	}
	previous, err := s.SourceLedger.History.DeletedPathContent(ctx, p.ID, deleted)
	if err != nil {
		return nil, err
	}
	currentPath, err := projectsource.ResolveAbsentSourcePath(p, req.RootID, req.Path)
	if err != nil {
		return nil, err
	}
	if currentPath != path {
		return nil, projectsource.ErrSourceNotFound
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
