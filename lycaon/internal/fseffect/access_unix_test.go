//go:build darwin || linux

package fseffect

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestReadRootAccessUsesEffectiveIdentityWithoutEffects(t *testing.T) {
	dir := t.TempDir()
	testutil.FailErr(t, "file fixture", os.WriteFile(filepath.Join(dir, "file"), []byte("exact fixture bytes"), 0600))
	testutil.FailErr(t, "directory fixture", os.Mkdir(filepath.Join(dir, "folder"), 0700))
	testutil.FailErr(t, "symlink fixture", os.Symlink("file", filepath.Join(dir, "link")))
	root, err := OpenReadRoot(dir)
	testutil.FailErr(t, "open admitted root", err)
	defer root.Close()
	for _, c := range []struct {
		path string
		mode AccessMode
		want bool
	}{
		{"file", ReadAccess, true}, {"file", WriteAccess, true}, {"file", ExecuteAccess, false},
		{"folder", ExecuteAccess, true}, {".", ExecuteAccess, true}, {"link", ReadAccess, true}, {"missing", ReadAccess, false}, {"file/child", ReadAccess, false}, {"file/", ReadAccess, false},
	} {
		got, err := root.Access(c.path, c.mode, nil)
		testutil.FailErr(t, "access query", err)
		if got != c.want {
			t.Fatalf("%s mode %d: %v want %v", c.path, c.mode, got, c.want)
		}
	}
	denied := errors.New("scope denied")
	seen := ""
	_, err = root.Access("link", ReadAccess, func(path string) error { seen = path; return denied })
	canonical, canonicalErr := filepath.EvalSymlinks(filepath.Join(dir, "file"))
	testutil.FailErr(t, "canonical fixture", canonicalErr)
	if !errors.Is(err, denied) || seen != canonical {
		t.Fatalf("resolved entry admission = %q, %v", seen, err)
	}
	if _, err := root.Access("file", AccessMode(99), nil); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("invalid access = %v", err)
	}
	bytes, err := os.ReadFile(filepath.Join(dir, "file"))
	testutil.FailErr(t, "read unchanged fixture", err)
	if string(bytes) != "exact fixture bytes" {
		t.Fatalf("query changed file %q", bytes)
	}
}
func TestReadRootAccessRetainsConfinementAndRootIdentity(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	testutil.FailErr(t, "outside fixture", os.WriteFile(filepath.Join(outside, "private"), []byte("private fixture"), 0600))
	testutil.FailErr(t, "held file", os.WriteFile(filepath.Join(dir, "private"), []byte("held"), 0600))
	testutil.FailErr(t, "escape link", os.Symlink(filepath.Join(outside, "private"), filepath.Join(dir, "escape")))
	root, err := OpenReadRoot(dir)
	testutil.FailErr(t, "open root", err)
	defer func() { _ = root.Close() }()
	for _, mode := range []AccessMode{ReadAccess, WriteAccess, ExecuteAccess} {
		_, err := root.Access("escape", mode, nil)
		if !errors.Is(err, ErrSymlink) {
			t.Fatalf("mode %d escaped/booleanized refusal %v", mode, err)
		}
	}
	link, err := root.Access(".", ExecuteAccess, nil)
	testutil.FailErr(t, "inspect final link only", err)
	if !link {
		t.Fatal("link metadata missing")
	}
	moved := dir + "-moved"
	testutil.FailErr(t, "move held root", os.Rename(dir, moved))
	defer func() { _ = os.RemoveAll(moved) }()
	testutil.FailErr(t, "replace root path", os.Mkdir(dir, 0700))
	got, err := root.Access("private", ReadAccess, nil)
	testutil.FailErr(t, "held descriptor observation", err)
	if !got {
		t.Fatal("probe followed replacement root")
	}
}
