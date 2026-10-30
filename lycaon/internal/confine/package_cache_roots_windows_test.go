package confine

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestStandardCacheDataRootsWindows(t *testing.T) {
	home := cacheRootFixture(t)
	assertCacheRootSet(t, home, filepath.Join(home, "config"), []string{
		filepath.Join(home, "cache"), filepath.Join(home, ".cache"),
		filepath.Join(home, "data"), filepath.Join(home, ".local/share"),
		filepath.Join(home, "state"), filepath.Join(home, ".local/state"),
	})
	t.Run("unset XDG conventions", func(t *testing.T) {
		t.Setenv("XDG_CACHE_HOME", "")
		t.Setenv("XDG_DATA_HOME", "")
		t.Setenv("XDG_STATE_HOME", "")
		assertCacheRootSet(t, home, filepath.Join(home, "config"), []string{
			filepath.Join(home, ".cache"), filepath.Join(home, ".local/share"), filepath.Join(home, ".local/state"),
		})
	})
	testutil.FailErr(t, "prepare platform caches", preparePackageCacheDataRoots(Confinement{}, func(fseffect.Location, os.FileMode) (int, error) {
		t.Error("platform cache preparation attempted a filesystem effect")
		return 0, nil
	}))
}
