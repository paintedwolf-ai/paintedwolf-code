//go:build linux && !android

package confine

import (
	"path/filepath"
	"testing"
)

func TestStandardCacheDataRootsLinux(t *testing.T) {
	home := cacheRootFixture(t)
	for _, data := range []string{"", filepath.Join(home, "data"), "relative-data"} {
		t.Run(data, func(t *testing.T) {
			t.Setenv("XDG_DATA_HOME", data)
			base := data
			if !filepath.IsAbs(base) {
				base = filepath.Join(home, ".local/share")
			}
			assertCacheRootSet(t, home, filepath.Join(home, "config"), []string{
				filepath.Join(home, "cache"), filepath.Join(home, ".cache"),
				filepath.Join(base, "pnpm/store"), filepath.Join(base, "NuGet/http-cache"), filepath.Join(base, "NuGet/plugin-cache"),
			})
		})
	}
	t.Run("unset cache convention", func(t *testing.T) {
		t.Setenv("XDG_CACHE_HOME", "")
		assertCacheRootSet(t, home, filepath.Join(home, "config"), []string{
			filepath.Join(home, ".cache"), filepath.Join(home, "data/pnpm/store"),
			filepath.Join(home, "data/NuGet/http-cache"), filepath.Join(home, "data/NuGet/plugin-cache"),
		})
	})
}
