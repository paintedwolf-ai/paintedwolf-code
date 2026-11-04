//go:build darwin || linux

package fseffect

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestLinkIntoStagingSharesTheFileAndRefusesEverythingElse(t *testing.T) {
	source, target := t.TempDir(), t.TempDir()
	testutil.FailErr(t, "write source", os.WriteFile(filepath.Join(source, "file"), []byte("bytes"), 0))
	from := Location{Root: source, Rel: "file"}

	testutil.FailErr(t, "link an unreadable file", LinkIntoStaging(from, Location{Root: target, Rel: "a/b/file"}))
	before, err := os.Lstat(filepath.Join(source, "file"))
	testutil.FailErr(t, "stat source", err)
	after, err := os.Lstat(filepath.Join(target, "a", "b", "file"))
	testutil.FailErr(t, "stat link", err)
	if !os.SameFile(before, after) || after.Mode().Perm() != 0 {
		t.Fatalf("link does not share the file: %v %v", before.Mode(), after.Mode())
	}
	if parent, err := os.Lstat(filepath.Join(target, "a")); err != nil || parent.Mode().Perm() != 0o700 {
		t.Fatalf("created parent %v err=%v", parent, err)
	}

	if err := LinkIntoStaging(from, Location{Root: target, Rel: "a/b/file"}); !errors.Is(err, os.ErrExist) {
		t.Fatalf("link must not replace: %v", err)
	}
	testutil.FailErr(t, "make symlink", os.Symlink("file", filepath.Join(source, "link")))
	if err := LinkIntoStaging(Location{Root: source, Rel: "link"}, Location{Root: target, Rel: "link"}); !errors.Is(err, ErrPostcondition) {
		t.Fatalf("a symlink is not a regular file: %v", err)
	}
	if err := LinkIntoStaging(Location{Root: source, Rel: "absent"}, Location{Root: target, Rel: "absent"}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("absent source: %v", err)
	}
}
