//go:build (!linux && !darwin && !windows) || android || ios

package confine

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestStandardCacheDataRootsOther(t *testing.T) {
	home := cacheRootFixture(t)
	config, err := os.UserConfigDir()
	if err != nil {
		t.Fatalf("fixture user config directory: %v", err)
	}
	assertCacheRootSet(t, home, config, []string{
		filepath.Join(home, "cache"), filepath.Join(home, ".cache"),
		filepath.Join(home, "data"), filepath.Join(home, ".local/share"),
		filepath.Join(home, "state"), filepath.Join(home, ".local/state"),
	})
	t.Run("unset XDG conventions", func(t *testing.T) {
		t.Setenv("XDG_CACHE_HOME", "")
		t.Setenv("XDG_DATA_HOME", "")
		t.Setenv("XDG_STATE_HOME", "")
		assertCacheRootSet(t, home, config, []string{
			filepath.Join(home, ".cache"), filepath.Join(home, ".local/share"), filepath.Join(home, ".local/state"),
		})
	})
	testutil.FailErr(t, "prepare platform caches", preparePackageCacheDataRoots(Confinement{}, func(fseffect.Location, os.FileMode) error {
		t.Error("platform cache preparation attempted a filesystem effect")
		return nil
	}))
}
