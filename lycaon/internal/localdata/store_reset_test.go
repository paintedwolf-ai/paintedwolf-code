package localdata

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestResetStoreCoupledPreservesAppPreferencesDeviceConfigurationAndDebugCaptures(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, storeFileName)
	var removed []string
	for _, rel := range storeCoupledRelDirs {
		removed = append(removed, filepath.Join(rel, "marker"))
	}
	removed = append(removed, storeCoupledRelPaths...)
	preserved := []string{FirstRunOnboardingRelPath(), "debug/sessions/keep.json"}
	for _, rel := range durableRelPaths {
		if !isStoreFile(rel) {
			preserved = append(preserved, rel)
		}
	}
	for _, rel := range append(append([]string(nil), removed...), preserved...) {
		path := filepath.Join(root, rel)
		testutil.FailErr(t, "mkdir "+rel, os.MkdirAll(filepath.Dir(path), 0o700))
		testutil.FailErr(t, "write "+rel, os.WriteFile(path, []byte("state"), 0o600))
	}

	testutil.FailErr(t, "reset", ResetStoreCoupled(dbPath))
	for _, rel := range removed {
		if _, err := os.Stat(filepath.Join(root, rel)); !os.IsNotExist(err) {
			t.Fatalf("store-coupled path %s survived: %v", rel, err)
		}
	}
	for _, rel := range preserved {
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			t.Fatalf("preserved path %s removed: %v", rel, err)
		}
	}
}
