package projectsource

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestAbsentSourcePathRequiresMissingJailedLocation(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	p := &Project{ID: "p", Roots: []Root{{ID: "r", Path: root}}}
	testutil.FailErr(t, "existing file", os.WriteFile(filepath.Join(root, "present"), nil, 0600))
	testutil.FailErr(t, "dangling symlink", os.Symlink(filepath.Join(root, "gone"), filepath.Join(root, "dangling")))
	testutil.FailErr(t, "escaped parent", os.Symlink(outside, filepath.Join(root, "escape")))
	testutil.FailErr(t, "directory", os.Mkdir(filepath.Join(root, "directory"), 0700))
	for _, path := range []string{"missing", "gone/parent/missing"} {
		_, err := ResolveAbsentSourcePath(p, "r", path)
		testutil.FailErr(t, "validate absent path", err)
	}
	for _, path := range []string{"../missing", "present", "directory", "dangling", "escape/missing", outside} {
		if _, err := ResolveAbsentSourcePath(p, "r", path); err == nil {
			t.Fatalf("accepted %q", path)
		}
	}
	if _, err := ResolveAbsentSourcePath(p, "other", "missing"); err == nil {
		t.Fatal("accepted detached root")
	}
	if _, err := ResolveAbsentSourcePath(p, "", "missing"); err == nil {
		t.Fatal("accepted unpinned root")
	}
}
