package board

import (
	"context"
	"encoding/json"
	"time"

	"github.com/lycaon/lycaon/internal/board/factscope"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/standingpatterns"
	"github.com/lycaon/lycaon/pkg/api"
)

type repositoryScopeKey struct {
	builder                   *SnapshotBuilder
	project, workspace, roots string
}

type repositoryFacts struct {
	repo                            api.RepoBrief
	roots                           []api.BoardOrientationRoot
	git                             *api.BoardGitSlice
	scans                           *api.BoardScansSlice
	flags                           []standingpatterns.FlagCount
	repoMs, gitMs, scansMs, flagsMs int64
	gitCacheHit                     bool
	changeSignal                    repochange.Source
}

func (b *SnapshotBuilder) repositoryFacts(ctx context.Context, project, workspace string, roots []projectroot.RootRef) (*repositoryFacts, bool, error) {
	encodedRoots, err := json.Marshal(roots)
	if err != nil {
		return nil, false, err
	}
	key := repositoryScopeKey{builder: b, project: project, workspace: workspace, roots: string(encodedRoots)}
	return factscope.Read(ctx, key, func() (*repositoryFacts, error) {
		return b.readRepositoryFacts(ctx, project, workspace, roots)
	})
}

func (b *SnapshotBuilder) readRepositoryFacts(ctx context.Context, project, workspace string, roots []projectroot.RootRef) (*repositoryFacts, error) {
	facts := &repositoryFacts{}
	started := time.Now()
	repo, orientation, materialized, err := b.loadOrientation(ctx, workspace, roots)
	if err != nil {
		return nil, err
	}
	facts.repo, facts.roots, facts.repoMs = *repo, orientation, time.Since(started).Milliseconds()
	started = time.Now()
	facts.git, facts.gitCacheHit, facts.changeSignal = b.loadGit(ctx, project, workspace, roots)
	facts.gitMs = time.Since(started).Milliseconds()
	started = time.Now()
	facts.scans = b.loadScans(ctx, scanPathsFromRoots(roots, workspace))
	facts.scansMs = time.Since(started).Milliseconds()
	if materialized {
		started = time.Now()
		facts.flags = b.standingFlags(ctx, b.OverlayGate.Dir(ctx, project, workspace))
		facts.flagsMs = time.Since(started).Milliseconds()
	}
	return facts, nil
}
