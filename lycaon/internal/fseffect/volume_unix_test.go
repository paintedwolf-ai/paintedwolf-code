//go:build darwin || linux

package fseffect

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"golang.org/x/sys/unix"
)

// TestVolumeOfReportsTheHoldingFileSystem reads a real directory's volume
// through a metadata descriptor and checks it against statfs and the mount
// point's device.
func TestVolumeOfReportsTheHoldingFileSystem(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve temp dir", err)
	root, err := OpenMetadataRoot(dir)
	testutil.FailErr(t, "open metadata root", err)
	defer func() { _ = root.Close() }()
	file, err := root.OpenExact(".", true)
	testutil.FailErr(t, "open metadata descriptor", err)
	defer func() { _ = file.Close() }()
	vol, err := VolumeOf(file, dir)
	testutil.FailErr(t, "volume", err)
	var st unix.Statfs_t
	testutil.FailErr(t, "statfs", unix.Statfs(dir, &st))
	if vol.Blocks != st.Blocks || vol.BlockSize == 0 || vol.Source == "" || vol.Source == "-" {
		t.Fatalf("volume %+v, statfs %+v", vol, st)
	}
	mount, err := os.Stat(vol.Target)
	testutil.FailErr(t, "stat mount point", err)
	held, err := os.Stat(dir)
	testutil.FailErr(t, "stat temp dir", err)
	if mount.Sys().(*syscall.Stat_t).Dev != held.Sys().(*syscall.Stat_t).Dev {
		t.Fatalf("mount point %q is not the device holding %q", vol.Target, dir)
	}
}
