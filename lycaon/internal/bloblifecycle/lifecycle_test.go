package bloblifecycle

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestDeviceAliasesShareLifecycleBeforeDirectoryCreation(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	testutil.FailErr(t, "create root alias", os.Symlink(root, "alias"))
	canonical := ForDevice(filepath.Join(root, "device"))
	for _, path := range []string{"device", filepath.Join(root, "alias", "device")} {
		if ForDevice(path) != canonical {
			t.Fatalf("device %q uses a different lifecycle", path)
		}
	}
}
