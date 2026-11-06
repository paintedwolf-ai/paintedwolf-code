//go:build linux

package workerplacement

import (
	"os"
	"path/filepath"
	"testing"
)

func assertExcluded(t *testing.T, dir string) {
	t.Helper()
	for _, name := range []string{"CACHEDIR.TAG", ".trackerignore"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestMountPointOfTakesTheLongestAndTheLastEntry(t *testing.T) {
	const mountinfo = `22 1 8:1 / / rw - ext4 /dev/sda1 rw
23 22 0:5 / /mnt/My\040Disk rw - ext4 /dev/sdb1 rw
24 22 0:6 / /tmp rw - tmpfs tmpfs rw
25 24 0:7 / /tmp rw - tmpfs tmpfs rw
`
	for path, want := range map[string]string{
		"/home/x":              "/",
		"/mnt/My Disk/project": "/mnt/My Disk",
		"/mnt/My Diskette":     "/",
		"/tmp/a":               "/tmp",
	} {
		if got := mountPointOf(mountinfo, path); got != want {
			t.Fatalf("%s: got %q, want %q", path, got, want)
		}
	}
}
