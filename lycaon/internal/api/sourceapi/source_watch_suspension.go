package sourceapi

import (
	"context"
	"sync"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/sourcefeed"
)

func (s *Watch) bindWorkspaceWatch(ctx context.Context, p *project.Project) bool {
	s.watchBindingMu.Lock()
	defer s.watchBindingMu.Unlock()
	if s.suspendedWatches[p.ID] > 0 {
		return false
	}
	current, err := s.ProjectRegistry.Get(ctx, p.ID)
	if err != nil || current.RootsGeneration != p.RootsGeneration {
		return false
	}
	roots := make([]sourcefeed.RootSpec, 0, len(p.Roots))
	for _, r := range p.Roots {
		roots = append(roots, sourcefeed.RootSpec{ID: r.ID, WorkspaceID: p.WorkspaceID(), Path: r.Path})
	}
	return sourcefeed.EnsureProjectWatch(ctx, &s.watches, p.ID, p.SourceBranch.String(), roots, func(ctx context.Context, _ string, batch sourcefeed.ExternalBatch) {
		s.observeWorkspaceChanges(ctx, p, batch)
	})
}

// SuspendProjectWatch closes binding admission and drains callbacks until resume.
func (s *Watch) SuspendProjectWatch(ctx context.Context, projectID string) (func(), error) {
	s.watchBindingMu.Lock()
	if s.suspendedWatches == nil {
		s.suspendedWatches = make(map[string]int)
	}
	s.suspendedWatches[projectID]++
	s.watchBindingMu.Unlock()
	resume := sync.OnceFunc(func() {
		s.watchBindingMu.Lock()
		defer s.watchBindingMu.Unlock()
		if s.suspendedWatches[projectID] <= 1 {
			delete(s.suspendedWatches, projectID)
		} else {
			s.suspendedWatches[projectID]--
		}
	})
	return resume, sourcefeed.StopProjectWatch(ctx, projectID)
}

// A newly bound stream cannot account for edits made while it was absent.
func resyncWorkspaceWatch(ctx context.Context, p *project.Project) {
	for _, root := range p.Roots {
		repochange.Notify(ctx, repochange.Event{
			ProjectDir: root.Path, Kind: repochange.WorktreeChanged, Source: repochange.SourceWatcher,
			Changes: []repochange.WorktreeChange{{Path: ".", Kind: repochange.WorktreeChangeResync}},
		})
	}
}
