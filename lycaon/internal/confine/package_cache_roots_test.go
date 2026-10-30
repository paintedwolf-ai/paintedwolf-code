package confine

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/fspath"
)

func cacheRootFixture(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("home", home)
	for name, relative := range map[string]string{
		"XDG_CACHE_HOME": "cache", "XDG_DATA_HOME": "data", "XDG_STATE_HOME": "state",
		"XDG_CONFIG_HOME": "config", "APPDATA": "config", "LYCAON_SANDBOX_WRITE_ROOTS": "granted",
	} {
		t.Setenv(name, filepath.Join(home, relative))
	}
	return home
}

func assertCacheRootSet(t *testing.T, home, config string, conventions []string) {
	t.Helper()
	want := append(slices.Clone(conventions),
		filepath.Join(home, ".bun/install/cache"), filepath.Join(home, ".npm/_cacache"),
		filepath.Join(home, ".cargo/registry"), filepath.Join(home, ".cargo/git"),
		filepath.Join(config, "go/telemetry"), fspath.CanonicalPath(filepath.Join(home, "granted")))
	got := standardCacheDataRoots()
	if !slices.Equal(got, want) {
		t.Errorf("default root set:\n got %q\nwant %q", got, want)
	}
}
