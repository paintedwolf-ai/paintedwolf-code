package projectadmin

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/project"
	sessioncheckpoint "github.com/lycaon/lycaon/internal/session/checkpoint"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/sourcesnapshot"
)

// releaseUnattachedSourceRoots gives up per-root source state for candidates
// no project attaches any more. Call it after the registry write, so the
// attachment picture excludes what was just removed. Source snapshots and scan
// demand for a root another project still holds are left alone.
func (s *Roots) releaseUnattachedSourceRoots(ctx context.Context, candidates []string) {
	if len(candidates) == 0 {
		return
	}
	held, err := s.attachedRootPaths(ctx)
	if err != nil {
		// Retaining is recoverable; the age-based sweep reclaims later.
		slog.WarnContext(ctx, "skip root state release; project list unavailable", "err", err)
		return
	}
	roots := make([]sourcesnapshot.Root, 0, len(candidates))
	for _, candidate := range candidates {
		path := filepath.Clean(strings.TrimSpace(candidate))
		if path == "" || path == "." {
			continue
		}
		if _, still := held[path]; still {
			continue
		}
		if err := s.ScanCadence.RetireRoot(ctx, path, s.rootStillAttached); err != nil {
			slog.WarnContext(ctx, "retire scans for unattached root", "path", path, "err", err)
		}
		roots = append(roots, sourcesnapshot.Root{Path: path})
	}
	if len(roots) == 0 {
		return
	}
	for _, root := range roots {
		if err := sourcecatalog.Process().Trees.ReleaseTreeRoot(ctx, root.Path); err != nil {
			slog.WarnContext(ctx, "release source catalog for unattached root", "path", root.Path, "err", err)
		}
	}
	if s.sourceWorkspace.SourceLedger == nil || s.sourceWorkspace.SourceLedger.SnapshotStore() == nil {
		return
	}
	if err := s.sourceWorkspace.SourceLedger.SnapshotStore().ReleaseRoots(ctx, roots); err != nil {
		slog.WarnContext(ctx, "release source snapshots for unattached roots", "err", err)
	}
}

func (s *Roots) rootStillAttached(ctx context.Context, root string) (bool, error) {
	held, err := s.attachedRootPaths(ctx)
	_, attached := held[root]
	return attached, err
}

// attachedRootPaths is every canonical root some project still holds.
func (s *Roots) attachedRootPaths(ctx context.Context) (map[string]struct{}, error) {
	projects, err := s.Registry.List(ctx)
	if err != nil {
		return nil, err
	}
	held := make(map[string]struct{}, len(projects))
	for i := range projects {
		for _, path := range rootPathsOf(&projects[i]) {
			held[filepath.Clean(path)] = struct{}{}
		}
	}
	return held, nil
}

func rootPathsOf(p *project.Project) []string {
	if p == nil {
		return nil
	}
	out := make([]string, 0, len(p.Roots))
	for _, root := range p.Roots {
		if path := strings.TrimSpace(root.Path); path != "" {
			out = append(out, path)
		}
	}
	return out
}

func (s *Roots) removeRootCheckpoints(ctx context.Context, projectID, root string) error {
	if err := s.Store.DropRootCheckpoints(ctx, projectID, enginepaths.ProjectKey(root)); err != nil {
		return err
	}
	held, err := s.attachedRootPaths(ctx)
	if err != nil {
		return err
	}
	if _, attached := held[filepath.Clean(root)]; attached {
		return nil
	}
	return sessioncheckpoint.RemoveProjectCheckpoints(s.DataDir, root)
}
