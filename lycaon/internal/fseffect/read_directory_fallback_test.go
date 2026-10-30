//go:build darwin || linux

package fseffect

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestDirectoryPathFallbackRequiresCurrentContainedIdentity(t *testing.T) {
	root := t.TempDir()
	testutil.FailErr(t, "create parent", os.MkdirAll(filepath.Join(root, "parent", "child"), 0700))
	reader, err := OpenReadRoot(root)
	testutil.FailErr(t, "open root", err)
	defer reader.Close()
	file, err := reader.OpenExact("parent/child")
	testutil.FailErr(t, "hold child and cache parent", err)
	defer file.Close()
	actual, err := reader.directoryPathFromName(file)
	testutil.FailErr(t, "validate unchanged child", err)
	if actual != file.Name() {
		t.Fatalf("native address=%q want %q", actual, file.Name())
	}
	testutil.FailErr(t, "move cached parent outside", os.Rename(filepath.Join(root, "parent"), filepath.Join(t.TempDir(), "outside")))
	testutil.FailErr(t, "replace parent", os.MkdirAll(filepath.Join(root, "parent", "child"), 0700))
	if _, err := reader.directoryPathFromName(file); !errors.Is(err, ErrSymlink) {
		t.Fatalf("fallback reused moved cached parent: %v", err)
	}
	testutil.FailErr(t, "close held child", file.Close())
	if _, err := reader.directoryPathFromName(file); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("closed fallback=%v", err)
	}
	external, err := os.Open(t.TempDir())
	testutil.FailErr(t, "open external directory", err)
	defer external.Close()
	if _, err := reader.directoryPathFromName(external); !errors.Is(err, ErrSymlink) {
		t.Fatalf("outside fallback=%v", err)
	}
}

func TestDirectoryPathNativeSpellingMismatchUsesContainedIdentity(t *testing.T) {
	root := t.TempDir()
	testutil.FailErr(t, "create child", os.Mkdir(filepath.Join(root, "child"), 0700))
	alias := filepath.Join(t.TempDir(), "root-alias")
	testutil.FailErr(t, "create equivalent root spelling", os.Symlink(root, alias))
	reader, err := OpenReadRoot(root)
	testutil.FailErr(t, "open root", err)
	defer reader.Close()
	// Model an equivalent root spelling without assuming volume case or Unicode rules.
	reader.root.path = alias
	file, err := reader.OpenExact("child")
	testutil.FailErr(t, "open child from equivalent spelling", err)
	defer file.Close()
	actual, err := reader.DirectoryPath(file)
	testutil.FailErr(t, "validate native spelling mismatch", err)
	if actual != filepath.Join(alias, "child") {
		t.Fatalf("admitted address=%q", actual)
	}
	moved := filepath.Join(t.TempDir(), "outside")
	testutil.FailErr(t, "move child outside", os.Rename(filepath.Join(root, "child"), moved))
	testutil.FailErr(t, "replace old spelling with outward link", os.Symlink(moved, filepath.Join(root, "child")))
	if _, err := reader.DirectoryPath(file); !errors.Is(err, ErrSymlink) {
		t.Fatalf("mismatch fallback followed outward replacement: %v", err)
	}
}
