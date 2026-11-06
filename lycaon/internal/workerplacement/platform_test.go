//go:build darwin || linux

package workerplacement

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestVolumeOfReportsAMountedAncestor(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve", err)
	volume, err := volumeOf(dir)
	testutil.FailErr(t, "volumeOf", err)
	// A macOS firmlink can put the path outside its mount point's name, so the
	// mount point's own identity is the check.
	root, err := volumeOf(volume.Root)
	testutil.FailErr(t, "volumeOf root", err)
	if root.ID != volume.ID || root.Root != volume.Root {
		t.Fatalf("root = %+v for %+v", root, volume)
	}
}

func TestExclusionsAreApplied(t *testing.T) {
	dir := t.TempDir()
	testutil.FailErr(t, "exclude", excludeFromBackupsAndIndexing(dir))
	assertExcluded(t, dir)
	if _, err := os.Stat(dir); err != nil {
		testutil.FailErr(t, "stat path", err)
	}
}
