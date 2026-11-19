package confine

import (
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/testutil"
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

// The fixture exercises cache authority outside the machine temp floor.
func privateCacheFixture(t *testing.T) string {
	t.Helper()
	account, err := user.Current()
	testutil.FailErr(t, "locate account home", err)
	dir, err := os.MkdirTemp(account.HomeDir, ".pw-cache-test-") //nolint:usetesting // Cache must lie outside shared temp.
	testutil.FailErr(t, "create cache fixture", err)
	t.Cleanup(func() { testutil.FailErr(t, "remove cache fixture", os.RemoveAll(dir)) })
	return dir
}

func TestSharedTempCacheOverlap(t *testing.T) {
	cache := privateCacheFixture(t)
	temp := filepath.Join(cache, "temporary")
	nestedCache := filepath.Join(temp, "nested-cache")
	attached := filepath.Join(temp, "attached")
	t.Setenv("XDG_CACHE_HOME", cache)
	t.Setenv("HOME", nestedCache)
	t.Setenv("TMPDIR", temp)
	rules, err := filesystemRules(Confinement{Roots: []string{attached}})
	testutil.FailErr(t, "compile cache rules", err)
	for _, path := range []string{filepath.Join(temp, "out"), filepath.Join(nestedCache, ".cargo", "registry", "index")} {
		if got := rules.Verdict(AccessWrite, path); got.Layer != FloorSharedTemp {
			t.Fatalf("cache overlap at %s: %+v", path, got)
		}
	}
	for _, path := range []string{filepath.Join(cache, "index"), filepath.Join(attached, "out")} {
		if !rules.Verdict(AccessWrite, path).Allowed {
			t.Errorf("write refused: %s", path)
		}
	}
}
