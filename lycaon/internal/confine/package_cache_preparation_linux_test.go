//go:build linux && !android

package confine

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/testutil"
)

func cachePreparationFixture(t *testing.T) Confinement {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("LYCAON_SANDBOX_WRITE_ROOTS", "")
	return Confinement{Roots: []string{filepath.Join(home, "project")}, Network: NetworkDeny}
}

func TestPackageCachePreparationCreatesOnlyAuthorizedStoreDirectories(t *testing.T) {
	c := cachePreparationFixture(t)
	data := os.Getenv("XDG_DATA_HOME")
	validated, err := validateProfileInputs(c)
	testutil.FailErr(t, "validate pure profile inputs", err)
	_, err = renderProfile(validated)
	testutil.FailErr(t, "render a pure profile", err)
	if _, err := os.Stat(data); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pure profile rendering materialized package directories: %v", err)
	}
	testutil.FailErr(t, "prepare first-use stores", preparePackageCacheDataRoots(c, fseffect.MkdirAll))
	for _, path := range packageCacheDataRoots() {
		info, err := os.Stat(path)
		testutil.FailErr(t, "inspect prepared store", err)
		if !info.IsDir() || info.Mode().Perm() != 0o700 {
			t.Fatalf("store is not a private directory: %v", info.Mode())
		}
	}
	for _, relative := range []string{"pnpm/bin", "applications", "systemd/user", "dbus-1/services"} {
		if _, err := os.Stat(filepath.Join(data, relative)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("store preparation created unrelated data: %s: %v", relative, err)
		}
	}
}

func TestPackageCachePreparationNeverOpensAnActualFloor(t *testing.T) {
	c := cachePreparationFixture(t)
	c.ReadRoots = packageCacheDataRoots()
	testutil.FailErr(t, "prepare with read-only store roots", preparePackageCacheDataRoots(c, fseffect.MkdirAll))
	for _, path := range packageCacheDataRoots() {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("preparation created a denied store: %v", err)
		}
	}
	protected := filepath.Join(os.Getenv("HOME"), ".ssh")
	testutil.FailErr(t, "create protected fixture", os.Mkdir(protected, 0o700))
	t.Setenv("XDG_DATA_HOME", protected)
	testutil.FailErr(t, "prepare with a key-material parent", preparePackageCacheDataRoots(c, fseffect.MkdirAll))
	entries, err := os.ReadDir(protected)
	testutil.FailErr(t, "inspect protected fixture", err)
	if len(entries) != 0 {
		t.Fatal("package preparation wrote inside the key-material floor")
	}
}

func TestPackageCachePreparationRefusesSymlinksBeforeAndAfterItsVerdict(t *testing.T) {
	for _, replace := range []bool{false, true} {
		t.Run(map[bool]string{false: "existing", true: "replaced"}[replace], func(t *testing.T) {
			c := cachePreparationFixture(t)
			data, protected := os.Getenv("XDG_DATA_HOME"), filepath.Join(os.Getenv("HOME"), ".ssh")
			testutil.FailErr(t, "create data fixture", os.Mkdir(data, 0o700))
			testutil.FailErr(t, "create protected fixture", os.Mkdir(protected, 0o700))
			installLink := func() {
				testutil.FailErr(t, "replace package parent with symlink", os.Symlink(protected, filepath.Join(data, "pnpm")))
			}
			if !replace {
				installLink()
			}
			first := true
			mkdir := func(location fseffect.Location, mode os.FileMode) error {
				if replace && first {
					first = false
					installLink()
				}
				return fseffect.MkdirAll(location, mode)
			}
			testutil.FailErr(t, "prepare with a retargeted parent", preparePackageCacheDataRoots(c, mkdir))
			entries, err := os.ReadDir(protected)
			testutil.FailErr(t, "inspect protected fixture", err)
			if len(entries) != 0 {
				t.Fatal("package preparation followed a symlink into the floor")
			}
		})
	}
}
