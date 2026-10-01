package git_test

import (
	"context"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/projectroot"
)

func scopeRepos(roots []projectroot.RootRef) []git.RepoRef {
	out := make([]git.RepoRef, 0, len(roots))
	for _, root := range roots {
		out = append(out, git.RepoRef{ID: root.Path, Toplevel: root.Path, Label: root.Label, RootIDs: []string{root.ID}, Available: true})
	}
	return out
}

func TestRepoSetCacheSeparatesRootIdentities(t *testing.T) {
	base := []projectroot.RootRef{{ID: "root", Path: "/project", Label: "Project", IsPrimary: true}}
	for _, changed := range []projectroot.RootRef{
		{ID: "root", Path: "/worktree", Label: "Project", IsPrimary: true},
		{ID: "other", Path: "/project", Label: "Project", IsPrimary: true},
		{ID: "root", Path: "/project", Label: "Renamed", IsPrimary: true},
		{ID: "root", Path: "/project", Label: "Project", IsPrimary: false},
	} {
		t.Run(changed.ID+changed.Path+changed.Label, func(t *testing.T) {
			var probes atomic.Int32
			cache := git.NewRepoSetCache(time.Hour, git.WithRepoSetDiscover(func(_ context.Context, roots []projectroot.RootRef) []git.RepoRef {
				probes.Add(1)
				return scopeRepos(roots)
			}))
			other := []projectroot.RootRef{changed}
			for _, roots := range [][]projectroot.RootRef{base, other, base, other} {
				got := cache.GetOrLoad(t.Context(), "project", 1, roots)
				if !reflect.DeepEqual(got, scopeRepos(roots)) {
					t.Fatalf("root set crossed cache boundary: got %+v for %+v", got, roots)
				}
				peek, ok := cache.PeekOrRevalidate(t.Context(), "project", 1, roots)
				if !ok || !reflect.DeepEqual(peek, got) {
					t.Fatalf("peek crossed cache boundary: %+v", peek)
				}
			}
			if probes.Load() != 2 {
				t.Fatalf("scope probes = %d, want 2", probes.Load())
			}
			cache.Invalidate("project")
			cache.GetOrLoad(t.Context(), "project", 1, base)
			cache.GetOrLoad(t.Context(), "project", 1, other)
			if probes.Load() != 4 {
				t.Fatalf("invalidation left a scope cached: probes %d", probes.Load())
			}
		})
	}
}

func TestRepoSetCacheDoesNotCoalesceDifferentCheckouts(t *testing.T) {
	started := make(chan string, 2)
	release := make(chan struct{})
	cache := git.NewRepoSetCache(time.Hour, git.WithRepoSetDiscover(func(_ context.Context, roots []projectroot.RootRef) []git.RepoRef {
		started <- roots[0].Path
		<-release
		return scopeRepos(roots)
	}))
	results := make(chan []git.RepoRef, 2)
	for _, path := range []string{"/project", "/worktree"} {
		go func() {
			results <- cache.GetOrLoad(t.Context(), "same-project", 0, []projectroot.RootRef{{ID: "root", Path: path}})
		}()
	}
	for range 2 {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			close(release)
			t.Fatal("different checkout discovery incorrectly joined one flight")
		}
	}
	close(release)
	seen := map[string]bool{}
	for range 2 {
		rows := <-results
		seen[rows[0].Toplevel] = true
	}
	if !seen["/project"] || !seen["/worktree"] {
		t.Fatalf("checkout discovery results mixed: %v", seen)
	}
}
