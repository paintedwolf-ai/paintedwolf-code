//go:build darwin || linux || windows

package fseffect

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestMkdirAllAssuresExistingRootWithoutMutation(t *testing.T) {
	root := t.TempDir()
	before, err := os.Stat(root)
	testutil.FailErr(t, "stat root", err)
	count, err := MkdirAll(Location{Root: root, Rel: "."}, 0o700)
	testutil.FailErr(t, "ensure root", err)
	after, err := os.Stat(root)
	testutil.FailErr(t, "stat assured root", err)
	if count != 0 || !os.SameFile(before, after) || before.Mode() != after.Mode() {
		t.Fatalf("existing root changed: count=%d before=%v after=%v", count, before, after)
	}
	if _, err := cleanLocation(Location{Root: root, Rel: "."}); err == nil {
		t.Fatal("root replacement validation was relaxed")
	}
}

func TestMkdirAllAssurancePreservesChildBoundaries(t *testing.T) {
	root := t.TempDir()
	testutil.FailErr(t, "seed collision", os.WriteFile(filepath.Join(root, "file"), []byte("keep"), 0o600))
	for _, rel := range []string{"", "../outside", "file", "file/nested"} {
		if count, err := MkdirAll(Location{Root: root, Rel: rel}, 0o755); err == nil || count != 0 {
			t.Fatalf("invalid directory %q: count=%d err=%v", rel, count, err)
		}
	}
	for attempt, want := range []int{2, 0} {
		count, err := MkdirAll(Location{Root: root, Rel: "one/two"}, 0o755)
		testutil.FailErr(t, "ensure nested directory", err)
		if count != want {
			t.Fatalf("attempt %d created %d directories; want %d", attempt, count, want)
		}
	}
	if _, err := MkdirAll(Location{Root: filepath.Join(root, "file"), Rel: "."}, 0o755); err == nil {
		t.Fatal("file accepted as root directory")
	}
}
