package contract

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/sensitivepath"
	"github.com/lycaon/lycaon/internal/testutil"
)

// Write-denied credential paths remain eligible for approval cards.
func TestCredentialFloorPathsAreInTheSensitiveCatalog(t *testing.T) {
	t.Parallel()
	home, err := os.UserHomeDir()
	testutil.FailErr(t, "os.UserHomeDir failed", err)
	catalog, err := sensitivepath.Load(sensitivepath.Bundled())
	testutil.FailErr(t, "sensitivepath.Load failed", err)

	for _, rel := range confine.KeyMaterialHomeRelPaths() {
		abs := filepath.Join(home, rel)
		if _, ok := catalog.Match(abs, sensitivepath.ModeWrite); !ok {
			t.Errorf("%q is write-denied by the sandbox floor but the sensitive-locations catalog "+
				"does not match it, so a refused write would raise no card", rel)
		}
	}
}

// Key-material reads remain catalogued for approval.
func TestKeyMaterialIsCataloguedForReads(t *testing.T) {
	t.Parallel()
	home, err := os.UserHomeDir()
	testutil.FailErr(t, "os.UserHomeDir failed", err)
	catalog, err := sensitivepath.Load(sensitivepath.Bundled())
	testutil.FailErr(t, "sensitivepath.Load failed", err)

	for _, rel := range []string{".ssh", ".gnupg", "Library/Keychains", ".aws", ".config/gcloud"} {
		if _, ok := catalog.Match(filepath.Join(home, rel), sensitivepath.ModeRead); !ok {
			t.Errorf("%q is not catalogued as read-sensitive, so nothing asks before it is read", rel)
		}
	}
}
