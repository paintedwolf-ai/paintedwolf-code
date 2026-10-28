package confine

import (
	"path/filepath"
	"testing"
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
	assertNoCachePreparation(t)
}
