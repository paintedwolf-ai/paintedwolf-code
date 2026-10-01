package git_test

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRepoSetCache_hitMissGenerationTTLInvalidate(t *testing.T) {
	var probes atomic.Int32
	fixture := []git.RepoRef{{ID: "r1", Toplevel: "/t", Label: "t", RootIDs: []string{"a"}, Available: true}}
	discover := func(context.Context, []projectroot.RootRef) []git.RepoRef {
		probes.Add(1)
		return append([]git.RepoRef(nil), fixture...)
	}
	now := time.Unix(1_700_000_000, 0)
	cache := git.NewRepoSetCache(git.DefaultStatusCacheTTL,
		git.WithRepoSetDiscover(discover),
		git.WithRepoSetCacheClock(func() time.Time { return now }),
	)
	roots := []projectroot.RootRef{{ID: "a", Path: "/t", IsPrimary: true}}

	_ = cache.GetOrLoad(context.Background(), "proj", 1, roots)
	_ = cache.GetOrLoad(context.Background(), "proj", 1, roots)
	if probes.Load() != 1 {
		t.Fatalf("same gen+TTL probes = %d want 1", probes.Load())
	}

	_ = cache.GetOrLoad(context.Background(), "proj", 2, roots)
	if probes.Load() != 2 {
		t.Fatalf("bumped generation probes = %d want 2", probes.Load())
	}

	now = now.Add(git.DefaultStatusCacheTTL + time.Millisecond)
	_ = cache.GetOrLoad(context.Background(), "proj", 2, roots)
	if probes.Load() != 3 {
		t.Fatalf("expired TTL probes = %d want 3", probes.Load())
	}

	cache.Invalidate("proj")
	_ = cache.GetOrLoad(context.Background(), "proj", 2, roots)
	if probes.Load() != 4 {
		t.Fatalf("after Invalidate probes = %d want 4", probes.Load())
	}
}

func TestRepoSetCache_singleflightConcurrent(t *testing.T) {
	var probes atomic.Int32
	var started sync.WaitGroup
	var release sync.WaitGroup
	started.Add(1)
	release.Add(1)
	discover := func(context.Context, []projectroot.RootRef) []git.RepoRef {
		probes.Add(1)
		started.Done()
		release.Wait()
		return []git.RepoRef{{ID: "r1", Toplevel: "/t", Label: "t", RootIDs: []string{"a"}, Available: true}}
	}
	cache := git.NewRepoSetCache(git.DefaultStatusCacheTTL, git.WithRepoSetDiscover(discover))
	roots := []projectroot.RootRef{{ID: "a", Path: "/t", IsPrimary: true}}

	const n = 8
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			_ = cache.GetOrLoad(context.Background(), "proj", 1, roots)
		}()
	}
	started.Wait()
	if probes.Load() != 1 {
		t.Fatalf("in-flight probes = %d want 1", probes.Load())
	}
	release.Done()
	wg.Wait()
	if probes.Load() != 1 {
		t.Fatalf("after join probes = %d want 1", probes.Load())
	}
}

func TestRepoSetCache_PeekOrRevalidateColdReturnsBeforeDiscovery(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	discover := func(context.Context, []projectroot.RootRef) []git.RepoRef {
		started <- struct{}{}
		<-release
		return []git.RepoRef{{ID: "r1", Toplevel: "/t", Available: true}}
	}
	cache := git.NewRepoSetCache(
		git.DefaultStatusCacheTTL,
		git.WithRepoSetDiscover(discover),
	)
	roots := []projectroot.RootRef{{ID: "a", Path: "/t", IsPrimary: true}}

	if repos, found := cache.PeekOrRevalidate(t.Context(), "proj", 1, roots); found || repos != nil {
		t.Fatalf("cold peek = (%v, %t), want (nil, false)", repos, found)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("background discovery did not start")
	}
	close(release)

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if repos, found := cache.PeekOrRevalidate(t.Context(), "proj", 1, roots); found {
			if len(repos) != 1 || repos[0].Toplevel != "/t" {
				t.Fatalf("warm peek = %#v", repos)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("background discovery did not publish")
}

// Two roots inside one checkout describe the same porcelain, so they reach one cache entry
// while each caller passes its own directory.
func TestStatusCache_sharedPorcelainAcrossRootsInOneCheckout(t *testing.T) {
	repo := initDiscoverRepo(t)
	web := filepath.Join(repo, "packages", "web")
	testutil.FailErr(t, "mkdir", os.MkdirAll(web, 0o755))

	ctx := context.Background()
	loader := &countingLoader{inner: git.NewManager()}
	cache := git.NewStatusCache(loader)
	_, err := cache.GetOrLoad(ctx, repo, false)
	testutil.FailErr(t, "load from the checkout top", err)
	_, err = cache.GetOrLoad(ctx, web, false)
	testutil.FailErr(t, "load from a package inside it", err)
	if loader.statusN.Load() != 1 {
		t.Fatalf("status execs = %d want 1 (one repository, one entry)", loader.statusN.Load())
	}
}

// Invalidation must land on the same key a subdirectory load used.
func TestStatusCache_invalidationReachesSubdirectoryRoots(t *testing.T) {
	repo := initDiscoverRepo(t)
	web := filepath.Join(repo, "packages", "web")
	testutil.FailErr(t, "mkdir", os.MkdirAll(web, 0o755))

	ctx := context.Background()
	loader := &countingLoader{inner: git.NewManager()}
	cache := git.NewStatusCache(loader)
	_, err := cache.GetOrLoad(ctx, web, false)
	testutil.FailErr(t, "load from a package inside the checkout", err)

	cache.InvalidateWithSource(repo, repochange.SourceMutation)

	_, err = cache.GetOrLoad(ctx, web, false)
	testutil.FailErr(t, "reload after invalidation at the top", err)
	if loader.statusN.Load() != 2 {
		t.Fatalf("status execs = %d want 2 (invalidation at the top reached the package root)", loader.statusN.Load())
	}
}
