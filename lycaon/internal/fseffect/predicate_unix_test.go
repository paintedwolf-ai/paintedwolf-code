//go:build darwin || linux

package fseffect

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestReadRootPredicatesObserveMetadataWithoutEffects(t *testing.T) {
	dir := t.TempDir()
	testutil.FailErr(t, "file fixture", os.WriteFile(filepath.Join(dir, "file"), []byte("exact fixture bytes"), 0600))
	testutil.FailErr(t, "directory fixture", os.Mkdir(filepath.Join(dir, "folder"), 0700))
	testutil.FailErr(t, "symlink fixture", os.Symlink("file", filepath.Join(dir, "link")))
	testutil.FailErr(t, "dangling link", os.Symlink("missing", filepath.Join(dir, "dangling")))
	root, err := OpenReadRoot(dir)
	testutil.FailErr(t, "open admitted root", err)
	defer func() { _ = root.Close() }()
	cases := []struct {
		path, op string
		want     bool
	}{{"file", "-e", true}, {"file", "-f", true}, {"file", "-d", false}, {"folder", "-d", true}, {"link", "-f", true}, {"link", "-L", true}, {"dangling", "-L", true}, {"dangling", "-e", false}, {"missing", "-e", false}, {"file/child", "-e", false}, {".", "-d", true}, {"file", "-r", true}, {"file", "-w", true}, {"file", "-x", false}, {"folder", "-x", true}}
	for _, c := range cases {
		t.Run(c.op+" "+c.path, func(t *testing.T) {
			got, err := root.TestPath(c.path, c.op)
			testutil.FailErr(t, "metadata/access probe", err)
			if got != c.want {
				t.Fatalf("got %v want %v", got, c.want)
			}
		})
	}
	bytes, err := os.ReadFile(filepath.Join(dir, "file"))
	testutil.FailErr(t, "read unchanged fixture", err)
	if string(bytes) != "exact fixture bytes" {
		t.Fatalf("query changed file %q", bytes)
	}
}
func TestReadRootPredicatesRetainConfinementAndRootIdentity(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	testutil.FailErr(t, "outside fixture", os.WriteFile(filepath.Join(outside, "private"), []byte("private fixture"), 0600))
	testutil.FailErr(t, "escape link", os.Symlink(filepath.Join(outside, "private"), filepath.Join(dir, "escape")))
	root, err := OpenReadRoot(dir)
	testutil.FailErr(t, "open root", err)
	defer func() { _ = root.Close() }()
	for _, op := range []string{"-e", "-f", "-r", "-w", "-x"} {
		_, err := root.TestPath("escape", op)
		if !errors.Is(err, ErrSymlink) {
			t.Fatalf("%s escaped/booleanized refusal %v", op, err)
		}
	}
	link, err := root.TestPath("escape", "-L")
	testutil.FailErr(t, "inspect final link only", err)
	if !link {
		t.Fatal("link metadata missing")
	}
	moved := dir + "-moved"
	testutil.FailErr(t, "move held root", os.Rename(dir, moved))
	defer func() { _ = os.RemoveAll(moved) }()
	testutil.FailErr(t, "replace root path", os.Mkdir(dir, 0700))
	got, err := root.TestPath("escape", "-L")
	testutil.FailErr(t, "held descriptor observation", err)
	if !got {
		t.Fatal("probe followed replacement root")
	}
}
