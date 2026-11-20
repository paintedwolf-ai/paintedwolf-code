package contract

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/detectionpack"
	"github.com/lycaon/lycaon/internal/sensitivepath"
	"github.com/lycaon/lycaon/internal/testutil"
)

// Write-denied credential paths remain eligible for approval cards.
func TestCredentialFloorPathsAreInTheSensitiveCatalog(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	catalog, err := sensitivepath.Load(sensitivepath.Bundled())
	testutil.FailErr(t, "sensitivepath.Load failed", err)

	paths, err := detectionpack.BundledKeyMaterialPaths()
	testutil.FailErr(t, "load key material floor", err)
	boundary := confine.BoundaryOf(&confine.Confinement{Roots: []string{t.TempDir()}})
	if !boundary.Applied {
		t.Fatal("credential floor fixture did not create a boundary")
	}
	for _, path := range paths {
		rel, ok := strings.CutPrefix(path, "~/")
		if !ok || rel == "" || filepath.IsAbs(rel) {
			t.Errorf("key material floor has an invalid home-relative path %q", path)
			continue
		}
		abs := filepath.Join(home, rel)
		if got := boundary.Filesystem.Verdict(confine.AccessWrite, abs); got != (confine.FloorVerdict{Layer: confine.FloorProtected}) {
			t.Errorf("%q write verdict = %+v, want protected", path, got)
		}
		if _, ok := catalog.Match(abs, sensitivepath.ModeWrite); !ok {
			t.Errorf("%q is missing from the write approval catalog", path)
		}
	}
}

// Key-material reads remain catalogued for approval.
func TestKeyMaterialIsCataloguedForReads(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	catalog, err := sensitivepath.Load(sensitivepath.Bundled())
	testutil.FailErr(t, "sensitivepath.Load failed", err)

	for _, rel := range []string{".ssh", ".gnupg", "Library/Keychains", ".aws", ".config/gcloud"} {
		if _, ok := catalog.Match(filepath.Join(home, rel), sensitivepath.ModeRead); !ok {
			t.Errorf("%q is not catalogued as read-sensitive, so nothing asks before it is read", rel)
		}
	}
}
