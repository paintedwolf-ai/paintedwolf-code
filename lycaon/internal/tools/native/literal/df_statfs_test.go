//go:build darwin || linux

package literal

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"golang.org/x/sys/unix"
)

// TestStatVolumeReportsTheHoldingFileSystem reads a real directory's volume
// and checks it against statfs and the mount the platform names.
func TestStatVolumeReportsTheHoldingFileSystem(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve temp dir", err)
	file, err := os.Open(dir)
	testutil.FailErr(t, "open temp dir", err)
	defer func() { _ = file.Close() }()
	vol, err := statVolume(file, dir)
	testutil.FailErr(t, "stat volume", err)
	var st unix.Statfs_t
	testutil.FailErr(t, "statfs", unix.Statfs(dir, &st))
	if vol.blocks != st.Blocks || vol.blockSize == 0 {
		t.Fatalf("volume %+v, statfs %+v", vol, st)
	}
	if vol.target == "" || vol.target == "-" || vol.source == "" || vol.source == "-" {
		t.Fatalf("volume names %q on %q", vol.source, vol.target)
	}
	if rel, err := filepath.Rel(vol.target, dir); err != nil || filepath.IsAbs(rel) || rel == ".." || len(rel) > 2 && rel[:3] == "../" {
		t.Fatalf("mount point %q does not hold %q", vol.target, dir)
	}
}
