package toolhost

import (
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/extpackstest"
)

// A host cache replaces the runtime's default one, so the default stops
// observing process-wide repository changes.
func TestSetGitStatusCacheReleasesReplacedDefault(t *testing.T) {
	r, err := NewRuntime(RuntimeConfig{ConfigRoot: configlayout.FindModuleRoot(), Catalog: extpackstest.StockCatalog(t)})
	testutil.FailErr(t, "build tool runtime", err)
	releases := 0
	release := r.gitStatusCache.releaseOwned
	r.gitStatusCache.releaseOwned = func() { releases++; release() }

	r.SetGitStatusCache(r.gitStatusCache.Load())
	if releases != 0 {
		t.Fatalf("re-setting the default cache released it %d times", releases)
	}
	host := git.NewStatusCache(git.NewManager())
	r.SetGitStatusCache(host)
	if releases != 1 {
		t.Fatalf("replacing the default cache released it %d times, want 1", releases)
	}
	if r.gitStatusCache.Load() != host {
		t.Fatal("runtime does not serve the host cache")
	}
}
