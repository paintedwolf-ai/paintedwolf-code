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
	out := make([]wire.NavigationReference, 0, len(refs))
	for _, ref := range refs {
		out = append(out, s.resolveNavigationPath(ctx, p, branch, ref))
	}
	return out
}

func (s *Workspace) resolveNavigationPath(ctx context.Context, p *project.Project, branch sourcebranch.ID, ref wire.NavigationReference) wire.NavigationReference {
	ref.Deleted = false
	var file sourceledger.BranchHead
	if ref.FileID != "" {
		ref.Status = wire.NavigationUnavailable
		if s.SourceLedger == nil || p == nil || ref.ProjectID != p.ID || branch.IsWorker() {
			return ref
		}
		head, err := s.SourceLedger.History.ResolveHeadByFile(ctx, p.ID, sourcebranch.Trunk, ref.FileID)
		if err != nil {
			return ref
		}
		file = head
		ref.RootID, ref.Path = file.RootID, file.Path
	}
	scoped, err := admitOutside(ctx, s.SourceLedger, p, ref.RootID, ref.Path)
	if err != nil {
		ref.Status = wire.NavigationUnavailable
		return ref
	}
	ref = project.ResolveNavigation(ctx, scoped, []wire.NavigationReference{ref})[0]
	if ref.Status == wire.NavigationResolved && file.FileID != "" && (file.State == "absent" || ref.RootID != file.RootID || ref.Path != file.Path) {
		ref.Status = wire.NavigationUnavailable
	}
	if ref.Status != wire.NavigationMissing || s.SourceLedger == nil {
		return ref
	}
	rel, err := projectsource.ResolveAbsentSourcePath(scoped, ref.RootID, ref.Path)
	if err != nil {
		ref.Status = wire.NavigationUnavailable
		return ref
	}
	deleted, err := s.SourceLedger.History.ResolveDeletedPath(ctx, p.ID, branch, ref.RootID, rel)
	switch {
	case err == nil && (ref.FileID == "" || ref.FileID == deleted.FileID):
		ref.Status, ref.EntryKind, ref.Deleted = wire.NavigationResolved, wire.NavigationEntryKindFile, true
	case !errors.Is(err, sourceledger.ErrHistoryNotFound):
		ref.Status = wire.NavigationUnavailable
	}
	return ref
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
