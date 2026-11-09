package session

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
)

// Workspace snapshots spell every root in baseline grammar (primary bare,
// others "@label/rel") regardless of which root the session has active.
func TestSnapshotWorkspaceFromRootsKeysBaselineGrammar(t *testing.T) {
	primary, secondary := t.TempDir(), t.TempDir()
	testutil.FailErr(t, "seed primary", os.WriteFile(filepath.Join(primary, "x.txt"), []byte("primary\n"), 0o600))
	testutil.FailErr(t, "seed secondary", os.WriteFile(filepath.Join(secondary, "y.txt"), []byte("shared\n"), 0o600))
	roots := []projectroot.RootRef{
		{ID: "p", Label: "app", Path: primary, IsPrimary: true},
		{ID: "s", Label: "shared", Path: secondary},
	}

	snapshot, err := SnapshotWorkspaceFromRoots(roots)
	testutil.FailErr(t, "snapshot workspace", err)
	keys := make([]string, 0, len(snapshot))
	for key := range snapshot {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	if !slices.Equal(keys, []string{"@shared/y.txt", "x.txt"}) {
		t.Fatalf("snapshot keys = %v", keys)
	}

	branch := t.TempDir()
	branchFiles := map[string]string{"p": "x.txt", "s": "y.txt"}
	branchContent := map[string]string{"p": "primary\n", "s": "shared\n"}
	for _, root := range roots {
		dir, err := projectroot.BranchDirForID(root.ID)
		testutil.FailErr(t, "branch dir", err)
		testutil.FailErr(t, "mkdir branch root", os.MkdirAll(filepath.Join(branch, dir), 0o750))
		target := filepath.Join(branch, dir, branchFiles[root.ID])
		testutil.FailErr(t, "seed branch file", os.WriteFile(target, []byte(branchContent[root.ID]), 0o600))
		// Fingerprints are mtime-based; an untouched branch copy keeps the
		// project file's timestamps.
		info, err := os.Stat(filepath.Join(root.Path, branchFiles[root.ID]))
		testutil.FailErr(t, "stat project file", err)
		testutil.FailErr(t, "preserve mtime", os.Chtimes(target, info.ModTime(), info.ModTime()))
	}
	secondaryDir, err := projectroot.BranchDirForID("s")
	testutil.FailErr(t, "secondary dir", err)
	testutil.FailErr(t, "change branch file", os.WriteFile(filepath.Join(branch, secondaryDir, "y.txt"), []byte("changed\n"), 0o600))

	changed := DiffBranchWorkspaceSnapshot(roots, branch, snapshot)
	if !slices.Equal(changed, []string{"@shared/y.txt"}) {
		t.Fatalf("changed = %v", changed)
	}
}
