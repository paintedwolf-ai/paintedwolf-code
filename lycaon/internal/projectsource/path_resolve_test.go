package projectsource

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestResolveProjectPathFileFolderAndRootIdentity(t *testing.T) {
	root := t.TempDir()
	testutil.FailErr(t, "make scripts folder", os.Mkdir(filepath.Join(root, "scripts"), 0o755))
	testutil.FailErr(t, "write script", os.WriteFile(filepath.Join(root, "scripts", "install-all.sh"), []byte("#!/bin/sh\n"), 0o600))
	p := &Project{Roots: []Root{{ID: "root-1", Path: root, IsPrimary: true}}}

	file, err := ResolveProjectPath(p, "", "./scripts/install-all.sh")
	testutil.FailErr(t, "resolve file", err)
	if file.RootID != "root-1" || file.Path != "scripts/install-all.sh" || file.EntryKind != ProjectPathFile {
		t.Fatalf("file = %+v", file)
	}
	folder, err := ResolveProjectPath(p, "", "scripts/")
	testutil.FailErr(t, "resolve folder", err)
	if folder.EntryKind != ProjectPathFolder || folder.Path != "scripts" {
		t.Fatalf("folder = %+v", folder)
	}
}

func TestResolveProjectPathFailsClosedOnAmbiguityAndEscape(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()
	for _, root := range []string{rootA, rootB} {
		testutil.FailErr(t, "write duplicate file", os.WriteFile(filepath.Join(root, "same.txt"), []byte("x"), 0o600))
	}
	p := &Project{Roots: []Root{
		{ID: "root-a", Path: rootA, IsPrimary: true},
		{ID: "root-b", Path: rootB},
	}}
	if _, err := ResolveProjectPath(p, "", "same.txt"); !errors.Is(err, ErrSourcePathAmbiguous) {
		t.Fatalf("ambiguous err = %v", err)
	}
	pinned, err := ResolveProjectPath(p, "root-b", "same.txt")
	testutil.FailErr(t, "resolve pinned duplicate", err)
	if pinned.RootID != "root-b" {
		t.Fatalf("pinned root = %q", pinned.RootID)
	}
	if _, err := ResolveProjectPath(p, "", "../outside.txt"); !errors.Is(err, ErrSourcePathDenied) {
		t.Fatalf("escape err = %v", err)
	}
	if _, err := ResolveProjectPath(p, "", ".git/config"); !errors.Is(err, ErrSourceNotFound) {
		t.Fatalf("metadata err = %v", err)
	}
}

func TestResolveProjectPathDeniesSymlinkOutsideRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	testutil.FailErr(t, "write outside file", os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o600))
	testutil.FailErr(t, "link outside folder", os.Symlink(outside, filepath.Join(root, "escape")))
	p := &Project{Roots: []Root{{ID: "root-1", Path: root, IsPrimary: true}}}
	if _, err := ResolveProjectPath(p, "", "escape/secret.txt"); !errors.Is(err, ErrSourcePathDenied) {
		t.Fatalf("symlink escape err = %v", err)
	}
}
