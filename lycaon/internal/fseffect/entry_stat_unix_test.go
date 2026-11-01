//go:build darwin || linux

package fseffect_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/testutil"
)

// TestReadRootStatReportsEntriesWithoutOpeningContent: lstat describes a
// link itself, even dangling or outward; follow resolves contained links and
// refuses outward ones.
func TestReadRootStatReportsEntriesWithoutOpeningContent(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	when := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	testutil.FailErr(t, "seed file", os.WriteFile(filepath.Join(root, "file"), []byte("12345"), 0o640))
	testutil.FailErr(t, "close content", os.Chmod(filepath.Join(root, "file"), 0o200))
	testutil.FailErr(t, "time file", os.Chtimes(filepath.Join(root, "file"), when, when))
	testutil.FailErr(t, "seed dir", os.Mkdir(filepath.Join(root, "dir"), 0o750))
	testutil.FailErr(t, "sticky dir", os.Chmod(filepath.Join(root, "dir"), 0o750|os.ModeSticky))
	testutil.FailErr(t, "inner link", os.Symlink("file", filepath.Join(root, "inner")))
	testutil.FailErr(t, "dangling link", os.Symlink("missing", filepath.Join(root, "dangling")))
	testutil.FailErr(t, "outward link", os.Symlink(outside, filepath.Join(root, "outward")))
	held, err := fseffect.OpenReadRoot(root)
	testutil.FailErr(t, "open root", err)
	defer held.Close()

	file, err := held.Stat("file", false, nil)
	testutil.FailErr(t, "stat write-only file", err)
	if file.Name != "file" || file.Size != 5 || file.Mode != 0o200 || !file.ModTime.Equal(when) || !file.POSIX || file.UID != uint32(os.Getuid()) || file.Links != 1 || file.Inode == 0 || (file.DeviceMajor == 0 && file.DeviceMinor == 0) {
		t.Fatalf("file = %+v", file)
	}
	dir, err := held.Stat("dir", false, nil)
	testutil.FailErr(t, "stat dir", err)
	if !dir.Mode.IsDir() || dir.Mode&os.ModeSticky == 0 || dir.Mode.Perm() != 0o750 {
		t.Fatalf("dir = %+v", dir)
	}
	for _, name := range []string{"inner", "dangling", "outward"} {
		link, err := held.Stat(name, false, nil)
		testutil.FailErr(t, "lstat "+name, err)
		if link.Mode&os.ModeSymlink == 0 {
			t.Fatalf("%s = %+v", name, link)
		}
	}
	followed, err := held.Stat("inner", true, nil)
	testutil.FailErr(t, "follow inner", err)
	if followed.Size != 5 || followed.Mode&os.ModeSymlink != 0 {
		t.Fatalf("followed = %+v", followed)
	}
	if _, err := held.Stat("outward", true, nil); !errors.Is(err, fseffect.ErrSymlink) {
		t.Fatalf("outward follow = %v", err)
	}
	if _, err := held.Stat("dangling", true, nil); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dangling follow = %v", err)
	}
	self, err := held.Stat(".", false, nil)
	testutil.FailErr(t, "stat root", err)
	if !self.Mode.IsDir() {
		t.Fatalf("root = %+v", self)
	}
	denied := errors.New("denied")
	var admitted []string
	_, err = held.Stat("dir", false, func(path string) error { admitted = append(admitted, path); return denied })
	if !errors.Is(err, denied) || len(admitted) != 1 || filepath.Base(admitted[0]) != "dir" {
		t.Fatalf("admission = %v %v", err, admitted)
	}
	if _, err := held.Stat(".", false, func(string) error { return denied }); !errors.Is(err, denied) {
		t.Fatalf("root admission = %v", err)
	}
	if _, err := held.Stat("/abs", false, nil); !errors.Is(err, fseffect.ErrInvalidPath) {
		t.Fatalf("absolute = %v", err)
	}
}
