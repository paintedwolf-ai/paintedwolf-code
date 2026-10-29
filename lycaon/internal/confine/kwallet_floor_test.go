//go:build darwin || linux

package confine_test

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/detectionpack"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestDefaultConfinementProtectsBundledKWalletStore(t *testing.T) {
	confine.TestingSetAutoConfine(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("LYCAON_CONFIG_DIR", filepath.Join(home, ".config", "paintedwolf"))
	t.Setenv("LYCAON_BYPASS_APPROVALS", "")
	t.Setenv("LYCAON_SANDBOX", "")
	t.Setenv("LYCAON_SANDBOX_DENY_READ", "")
	paths, err := detectionpack.BundledKeyMaterialPaths()
	testutil.FailErr(t, "load bundled key-material floor", err)
	confine.SetKeyMaterialPathsSource(func() []string { return paths })
	t.Cleanup(func() { confine.SetKeyMaterialPathsSource(func() []string { return keyMaterialTestFloor }) })

	store := filepath.Join(home, ".local", "share", "kwalletd")
	sibling := filepath.Join(home, ".local", "share", "kwalletd-backup", "kdewallet.kwl")
	for _, name := range []string{"kdewallet.kwl", "kdewallet.salt"} {
		writeFixtureFile(t, filepath.Join(store, name))
	}
	writeFixtureFile(t, sibling)
	c, on := confine.DefaultConfinement(confine.Request{Roots: []string{t.TempDir()}})
	if !on || c == nil {
		t.Fatal("default confinement must be available")
	}
	boundary := confine.BoundaryOf(c)
	for _, name := range []string{"kdewallet.kwl", "kdewallet.salt"} {
		path := filepath.Join(store, name)
		for access, want := range map[confine.FilesystemAccess]confine.FloorVerdict{
			confine.AccessRead:  {Layer: confine.FloorReadKeyMaterial},
			confine.AccessWrite: {Layer: confine.FloorProtected},
		} {
			if got := boundary.Filesystem.Verdict(access, path); got != want {
				t.Errorf("%s %s = %+v, want %+v", access, name, got, want)
			}
		}
		if got := confine.ClassifyBlockedWrite(path); got.Kind != confine.WriteSubjectKeyMaterial {
			t.Errorf("%s write subject = %s, want key material", name, got.Kind)
		}
	}
	if confine.KeyMaterialPath(sibling) {
		t.Fatal("a sibling directory must not match the key-material floor")
	}
	if got := boundary.Filesystem.Verdict(confine.AccessRead, sibling); !got.Allowed {
		t.Errorf("sibling read = %+v, want allowed", got)
	}
	if got := confine.ClassifyBlockedWrite(sibling); got.Kind != confine.WriteSubjectOrdinary {
		t.Errorf("sibling write subject = %s, want ordinary", got.Kind)
	}
}
