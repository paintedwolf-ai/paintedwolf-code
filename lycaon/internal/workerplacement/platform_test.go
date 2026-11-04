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
	if volume.Root == "" || !atOrUnder(dir, volume.Root) {
		t.Fatalf("root = %q for %s", volume.Root, dir)
	}
}

func TestExclusionsAreApplied(t *testing.T) {
	dir := t.TempDir()
	testutil.FailErr(t, "exclude", excludeFromBackupsAndIndexing(dir))
	assertExcluded(t, dir)
	if _, err := os.Stat(dir); err != nil {
		t.Fatal(err)
	}
}
