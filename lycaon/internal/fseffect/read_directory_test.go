//go:build darwin || linux

package fseffect_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestHeldDirectoryOpensAreRelativeAndCounted(t *testing.T) {
	root := t.TempDir()
	testutil.FailErr(t, "create directories", os.MkdirAll(filepath.Join(root, "a", "b"), 0700))
	reader, err := fseffect.OpenReadRoot(root)
	testutil.FailErr(t, "open root", err)
	defer reader.Close()
	parent, err := reader.OpenExact(".")
	testutil.FailErr(t, "open parent", err)
	defer parent.Close()
	child, err := reader.OpenChildDirectory(parent, "a")
	testutil.FailErr(t, "open child", err)
	defer child.Close()
	grandchild, err := reader.OpenChildDirectory(child, "b")
	testutil.FailErr(t, "open grandchild", err)
	testutil.FailErr(t, "close grandchild", grandchild.Close())
	if reader.DescriptorOpens() != 3 {
		t.Fatalf("descriptor opens=%d, want 3", reader.DescriptorOpens())
	}
	for _, name := range []string{"", ".", "..", "a/b", "bad\x00name"} {
		if _, err := reader.StatChildAt(parent, name, name, nil); !errors.Is(err, fseffect.ErrInvalidPath) {
			t.Fatalf("metadata name=%q err=%v", name, err)
		}
		if _, err := reader.OpenChildDirectory(parent, name); !errors.Is(err, fseffect.ErrInvalidPath) {
			t.Fatalf("name=%q error=%v", name, err)
		}
	}
}

func TestHeldDirectoryMetadataUsesCurrentNativeAuthority(t *testing.T) {
	root := t.TempDir()
	testutil.FailErr(t, "create old directory", os.Mkdir(filepath.Join(root, "old"), 0700))
	testutil.FailErr(t, "create entry", os.WriteFile(filepath.Join(root, "old", "entry"), []byte("original"), 0600))
	reader, err := fseffect.OpenReadRoot(root)
	testutil.FailErr(t, "open root", err)
	defer reader.Close()
	dir, err := reader.OpenExact("old")
	testutil.FailErr(t, "open directory", err)
	defer dir.Close()
	testutil.FailErr(t, "rename held directory", os.Rename(filepath.Join(root, "old"), filepath.Join(root, "renamed")))
	var admitted string
	stat, err := reader.StatChildAt(dir, "old/entry", "entry", func(path string) error { admitted = path; return nil })
	testutil.FailErr(t, "stat renamed child", err)
	if stat.Size != 8 || admitted != filepath.Join(reader.Path(), "renamed", "entry") {
		t.Fatalf("stat=%+v admitted=%q", stat, admitted)
	}
	outside := filepath.Join(t.TempDir(), "moved")
	testutil.FailErr(t, "move outside root", os.Rename(filepath.Join(root, "renamed"), outside))
	admitted = ""
	if _, err := reader.StatChildAt(dir, "old/entry", "entry", func(path string) error { admitted = path; return nil }); !errors.Is(err, fseffect.ErrSymlink) || admitted != "" {
		t.Fatalf("outside metadata err=%v admitted=%q", err, admitted)
	}
	if _, err := reader.OpenChildDirectory(dir, "entry"); !errors.Is(err, fseffect.ErrSymlink) {
		t.Fatalf("outside open err=%v", err)
	}
}

func TestHeldDirectoryChildSwapCannotEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	testutil.FailErr(t, "create parent", os.Mkdir(filepath.Join(root, "parent"), 0700))
	reader, err := fseffect.OpenReadRoot(root)
	testutil.FailErr(t, "open root", err)
	defer reader.Close()
	parent, err := reader.OpenExact("parent")
	testutil.FailErr(t, "open parent", err)
	defer parent.Close()
	testutil.FailErr(t, "outward link", os.Symlink(outside, filepath.Join(root, "parent", "child")))
	if file, err := reader.OpenChildDirectory(parent, "child"); !errors.Is(err, fseffect.ErrSymlink) {
		if file != nil {
			file.Close()
		}
		t.Fatalf("outward child err=%v", err)
	}
	testutil.FailErr(t, "contained target", os.Mkdir(filepath.Join(root, "target"), 0700))
	testutil.FailErr(t, "contained link", os.Symlink("../target", filepath.Join(root, "parent", "link")))
	child, err := reader.OpenChildDirectory(parent, "link")
	testutil.FailErr(t, "open contained child", err)
	testutil.FailErr(t, "close contained child", child.Close())
	testutil.FailErr(t, "close parent", parent.Close())
	if file, err := reader.OpenChildDirectory(parent, "child"); err == nil {
		file.Close()
		t.Fatal("closed parent remained usable")
	}
}
