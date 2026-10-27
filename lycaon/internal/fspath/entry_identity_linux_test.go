//go:build linux

package fspath

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestIdentityWithoutBirthTimeSurvivesRenameAndRejectsReuse(t *testing.T) {
	dir := t.TempDir()
	parent, err := unix.Open(dir, unix.O_DIRECTORY|unix.O_RDONLY|unix.O_CLOEXEC, 0)
	testutil.FailErr(t, "open directory", err)
	defer func() { _ = unix.Close(parent) }()
	testutil.FailErr(t, "seed entry", os.WriteFile(filepath.Join(dir, "entry"), nil, 0o600))
	first, err := identityWithoutBirthTime(parent, "entry")
	testutil.FailErr(t, "first identity", err)
	if !strings.Contains(first, ":h") {
		t.Fatalf("identity %q carries no filesystem handle on a filesystem that has handles", first)
	}
	testutil.FailErr(t, "rename entry", os.Rename(filepath.Join(dir, "entry"), filepath.Join(dir, "renamed")))
	if renamed, err := identityWithoutBirthTime(parent, "renamed"); err != nil || renamed != first {
		t.Fatalf("identity after rename = %q, %v; want %q", renamed, err, first)
	}
	var original unix.Stat_t
	testutil.FailErr(t, "stat entry", unix.Fstatat(parent, "renamed", &original, 0))
	testutil.FailErr(t, "remove entry", os.Remove(filepath.Join(dir, "renamed")))
	// Filesystems commonly hand a freed inode number to the next file; when
	// that happens the identity must still differ.
	for i := 0; i < 64; i++ {
		name := filepath.Join(dir, "next")
		testutil.FailErr(t, "create next", os.WriteFile(name, nil, 0o600))
		var next unix.Stat_t
		testutil.FailErr(t, "stat next", unix.Fstatat(parent, "next", &next, 0))
		if next.Ino == original.Ino {
			reused, err := identityWithoutBirthTime(parent, "next")
			testutil.FailErr(t, "reused identity", err)
			if reused == first {
				t.Fatalf("a reused inode number kept identity %q", first)
			}
			return
		}
		testutil.FailErr(t, "remove next", os.Remove(name))
	}
}
