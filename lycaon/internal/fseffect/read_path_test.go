//go:build darwin || linux

package fseffect

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestReadPathOperationsUseTheSameContainedWalk(t *testing.T) {
	dir := t.TempDir()
	testutil.FailErr(t, "create directory", os.Mkdir(filepath.Join(dir, "inside"), 0700))
	testutil.FailErr(t, "seed target", os.WriteFile(filepath.Join(dir, "inside", "target"), []byte("ok"), 0600))
	testutil.FailErr(t, "create directory alias", os.Symlink("inside", filepath.Join(dir, "alias")))
	testutil.FailErr(t, "create leaf link", os.Symlink("target", filepath.Join(dir, "inside", "link")))
	root, err := OpenReadRoot(dir)
	testutil.FailErr(t, "open contained root", err)
	defer root.Close()
	canonical, err := root.Canonical("alias/link", true)
	testutil.FailErr(t, "resolve contained canonical name", err)
	if canonical != filepath.Join(root.Path(), "inside", "target") {
		t.Fatalf("canonical=%q", canonical)
	}
	target, err := root.Readlink("alias/link", nil)
	testutil.FailErr(t, "read contained link metadata", err)
	if target != "target" {
		t.Fatalf("link text=%q", target)
	}
	testutil.FailErr(t, "create escaping parent link", os.Symlink(t.TempDir(), filepath.Join(dir, "outside")))
	if _, err := root.Readlink("outside/link", nil); !errors.Is(err, ErrSymlink) {
		t.Fatalf("escaping link metadata=%v", err)
	}
	if _, err := root.Canonical("outside/target", true); !errors.Is(err, ErrSymlink) {
		t.Fatalf("escaping canonical path=%v", err)
	}
}
