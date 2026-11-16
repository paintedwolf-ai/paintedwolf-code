package git_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/testutil"
)

// Two roots inside one checkout describe the same porcelain, so they reach one cache entry
// while each caller passes its own directory.
func TestStatusCache_sharedPorcelainAcrossRootsInOneCheckout(t *testing.T) {
	t.Parallel()
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

// Invalidation lands on the same key a subdirectory load used.
func TestStatusCache_invalidationReachesSubdirectoryRoots(t *testing.T) {
	t.Parallel()
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
