//go:build darwin || linux

package projectpaths

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestHeldChildDirectoryPreservesParentAndAdmitsRenamedPath(t *testing.T) {
	root := t.TempDir()
	testutil.FailErr(t, "create directories", os.MkdirAll(filepath.Join(root, "old", "child"), 0700))
	reader, err := fseffect.OpenReadRoot(root)
	testutil.FailErr(t, "open root", err)
	defer reader.Close()
	file, err := reader.OpenExact("old")
	testutil.FailErr(t, "open parent", err)
	denied := errors.New("new address denied")
	var admitted string
	path := BoundPath{Resolved: Resolved{Abs: filepath.Join(root, "old"), ScopeRel: "old", DisplayPath: "old"}, exactRelative: "old", admitRead: func(actual string) error {
		admitted = actual
		if filepath.Base(filepath.Dir(actual)) == "denied" {
			return denied
		}
		return nil
	}}
	dir := &BoundDir{dir: path, root: reader, file: file}
	defer dir.Close()
	child, err := dir.OpenChildDirectory("child")
	testutil.FailErr(t, "hold child", err)
	testutil.FailErr(t, "close child", child.Close())
	if _, err := file.Stat(); err != nil {
		t.Fatalf("child closed its parent: %v", err)
	}
	testutil.FailErr(t, "rename parent", os.Rename(filepath.Join(root, "old"), filepath.Join(root, "denied")))
	if _, err := dir.OpenChildDirectory("child"); !errors.Is(err, denied) || admitted != filepath.Join(reader.Path(), "denied", "child") {
		t.Fatalf("renamed child admission=%q err=%v", admitted, err)
	}
}

func TestHeldDirectoryCanResetBorrowedMembershipObservation(t *testing.T) {
	root := t.TempDir()
	testutil.FailErr(t, "seed entry", os.WriteFile(filepath.Join(root, "entry"), nil, 0600))
	testutil.FailErr(t, "seed remaining entry", os.WriteFile(filepath.Join(root, "second"), nil, 0600))
	reader, err := fseffect.OpenReadRoot(root)
	testutil.FailErr(t, "open root", err)
	defer reader.Close()
	file, err := reader.OpenExact(".")
	testutil.FailErr(t, "open directory", err)
	dir := &BoundDir{dir: BoundPath{}, root: reader, file: file}
	if _, err := dir.DirectoryFile().Readdirnames(1); err != nil {
		t.Fatalf("borrow membership: %v", err)
	}
	testutil.FailErr(t, "reset borrowed observation", dir.ResetListing())
	names, err := dir.Names(DirectoryNames{IncludeHidden: true})
	testutil.FailErr(t, "read reset membership", err)
	if len(names) != 2 || names[0] != "entry" || names[1] != "second" {
		t.Fatalf("reset membership=%v", names)
	}
	testutil.FailErr(t, "close directory", dir.Close())
	if err := dir.ResetListing(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("reset closed directory=%v", err)
	}
}
