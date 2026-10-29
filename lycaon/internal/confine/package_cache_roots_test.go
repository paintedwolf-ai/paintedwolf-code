package confine

import (
	"path/filepath"
	"runtime"
	"slices"
	"testing"
)

func TestStandardDataRootsKeepOnlyPackageStoresOnLinux(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("LYCAON_SANDBOX_WRITE_ROOTS", "")
	for _, data := range []string{"", filepath.Join(home, "custom-data"), "relative-data"} {
		t.Run(data, func(t *testing.T) {
			t.Setenv("XDG_DATA_HOME", data)
			state := filepath.Join(home, "custom-state")
			t.Setenv("XDG_STATE_HOME", state)
			roots := standardCacheDataRoots()
			base := data
			if !filepath.IsAbs(base) {
				base = filepath.Join(home, ".local/share")
			}
			if runtime.GOOS != "linux" {
				for _, want := range []string{filepath.Join(home, ".local/share"), filepath.Join(home, ".local/state"), state} {
					if !slices.Contains(roots, want) {
						t.Errorf("non-Linux data convention changed: missing %q", want)
					}
				}
				return
			}
			for _, rel := range []string{"pnpm/store", "NuGet/http-cache", "NuGet/plugin-cache"} {
				if !slices.Contains(roots, filepath.Join(base, rel)) {
					t.Errorf("package store missing: %s", rel)
				}
			}
			for _, path := range []string{base, state, filepath.Join(home, ".local/share"), filepath.Join(home, ".local/state"),
				filepath.Join(base, "pnpm"), filepath.Join(base, "NuGet"), filepath.Join(base, "pnpm/bin")} {
				if slices.Contains(roots, path) {
					t.Errorf("broad data, state or executable root admitted: %s", path)
				}
			}
		})
	}
}
