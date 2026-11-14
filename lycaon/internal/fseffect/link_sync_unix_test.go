//go:build darwin || linux

package fseffect

import (
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"golang.org/x/sys/unix"
)

// A retained name is recovery authority, so its inode must be durable before the
// name exists and the name must be durable when HardLink returns.
func TestHardLinkSyncsInodeBeforeNameAndParentAfter(t *testing.T) {
	root, retainRoot := t.TempDir(), t.TempDir()
	source := filepath.Join(root, "a.txt")
	final := filepath.Join(retainRoot, "slots", "slot")
	testutil.FailErr(t, "seed source", os.WriteFile(source, []byte("v1\n"), 0o600))
	var order []string
	err := hardLinkWithSync(HardLinkRequest{
		Source:   Location{Root: root, Rel: "a.txt"},
		Location: Location{Root: retainRoot, Rel: "slots/slot"},
		DirMode:  0o700,
	}, func(fd int) error {
		var stat unix.Stat_t
		testutil.FailErr(t, "fstat synced descriptor", unix.Fstat(fd, &stat))
		_, statErr := os.Lstat(final)
		switch stat.Mode & unix.S_IFMT {
		case unix.S_IFREG:
			if statErr == nil || stat.Nlink != 2 {
				t.Fatalf("inode synced with name present=%v nlink=%d", statErr == nil, stat.Nlink)
			}
			order = append(order, "inode")
		case unix.S_IFDIR:
			if statErr != nil {
				t.Fatalf("parent synced before rename: %v", statErr)
			}
			order = append(order, "parent")
		}
		return fsyncFD(fd)
	})
	testutil.FailErr(t, "hard link", err)
	if !slices.Equal(order, []string{"inode", "parent"}) {
		t.Fatalf("sync order = %v", order)
	}
}

func TestHardLinkSyncFailurePublishesNoName(t *testing.T) {
	root, retainRoot := t.TempDir(), t.TempDir()
	testutil.FailErr(t, "seed source", os.WriteFile(filepath.Join(root, "a.txt"), []byte("v1\n"), 0o600))
	err := hardLinkWithSync(HardLinkRequest{
		Source:   Location{Root: root, Rel: "a.txt"},
		Location: Location{Root: retainRoot, Rel: "slot"},
	}, func(int) error { return syscall.EIO })
	if err == nil {
		t.Fatal("sync failure ignored")
	}
	entries, readErr := os.ReadDir(retainRoot)
	testutil.FailErr(t, "read retained directory", readErr)
	if len(entries) != 0 {
		t.Fatalf("sync failure left %v", entries)
	}
}

func TestHardLinkUnavailableClassifiesCapabilityOnly(t *testing.T) {
	for _, err := range []error{ErrUnsupported, syscall.EXDEV, &os.LinkError{Err: syscall.ENOTSUP}, syscall.EOPNOTSUPP, syscall.EPERM, syscall.ENOSYS} {
		if !HardLinkUnavailable(err) {
			t.Errorf("%v not classified as unavailable", err)
		}
	}
	for _, err := range []error{nil, syscall.EIO, syscall.ENOSPC, syscall.EACCES, syscall.EMLINK, os.ErrExist} {
		if HardLinkUnavailable(err) {
			t.Errorf("%v classified as unavailable", err)
		}
	}
}
