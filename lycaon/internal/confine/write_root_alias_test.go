package confine_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/detectionpack"
	"github.com/lycaon/lycaon/internal/testutil"
)

// TestWriteRootRefusalHoldsForEverySpelling checks aliases of protected roots.
func TestWriteRootRefusalHoldsForEverySpelling(t *testing.T) {
	km, err := detectionpack.BundledKeyMaterialPaths()
	testutil.FailErr(t, "bundled key material", err)
	confine.SetKeyMaterialPathsSource(func() []string { return km })
	t.Cleanup(func() { confine.SetKeyMaterialPathsSource(nil) })

	cs, err := detectionpack.BundledCredentialStorePaths()
	testutil.FailErr(t, "bundled credential stores", err)
	confine.SetCredentialStorePathsSource(func() []string { return cs })
	t.Cleanup(func() { confine.SetCredentialStorePathsSource(nil) })

	home, err := os.UserHomeDir()
	testutil.FailErr(t, "home dir", err)
	configDir, err := configdir.UserConfigDir()
	if err != nil {
		t.Skipf("no user config dir: %v", err)
	}

	targets := []string{configDir, filepath.Join(home, ".ssh"), filepath.Join(home, ".gnupg")}
	checked := 0
	for _, target := range targets {
		if confine.ValidateGrantedWriteRoots([]string{target}) == nil {
			continue // not refused directly; nothing to keep consistent
		}
		for name, alias := range testutil.AliasSpellings(t, target) {
			checked++
			if confine.ValidateGrantedWriteRoots([]string{alias}) == nil {
				t.Errorf("%s via %s: %q is refused but the same directory spelled %q is accepted",
					target, name, target, alias)
			}
		}
	}
	t.Logf("checked %d alias spellings of refused write roots", checked)
}
