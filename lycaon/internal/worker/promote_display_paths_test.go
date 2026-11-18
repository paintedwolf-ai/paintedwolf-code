package worker

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestPromotePreservesEscapedPrimaryRootNames(t *testing.T) {
	primary, secondary, branch := t.TempDir(), t.TempDir(), t.TempDir()
	roots := []projectroot.RootRef{{ID: "p", Label: "app", Path: primary, IsPrimary: true}, {ID: "s", Label: "library", Path: secondary}}
	primaryBranch, err := projectroot.BranchDirForID("p")
	testutil.FailErr(t, "primary branch directory", err)
	secondaryBranch, err := projectroot.BranchDirForID("s")
	testutil.FailErr(t, "secondary branch directory", err)
	for path, content := range map[string]string{
		filepath.Join(primary, "@library/file.txt"):               "primary literal",
		filepath.Join(secondary, "file.txt"):                      "secondary",
		filepath.Join(branch, primaryBranch, "@library/file.txt"): "literal branch",
		filepath.Join(branch, secondaryBranch, "file.txt"):        "secondary branch",
	} {
		testutil.FailErr(t, "create fixture directory", os.MkdirAll(filepath.Dir(path), 0700))
		testutil.FailErr(t, "write fixture", os.WriteFile(path, []byte(content), 0600))
	}
	task := &api.WorkerTask{WorkspacePath: primary, WorkspaceRoot: branch, WorkspaceRootID: "p"}
	promote := PromoteRootsForTask(task, roots)
	for _, tc := range []struct{ path, primary, branch string }{{"./@library/file.txt", "primary literal", "literal branch"}, {"@library/file.txt", "secondary", "secondary branch"}} {
		current, modified, present, err := promote.ReadPairBytes(task, tc.path)
		testutil.FailErr(t, "read rooted pair", err)
		if !present || string(current) != tc.primary || string(modified) != tc.branch {
			t.Fatalf("%s = %q/%q, present=%v", tc.path, current, modified, present)
		}
	}
	testutil.FailErr(t, "write escaped primary path", promote.writePrimaryBytes(task, "./@library/file.txt", []byte("promoted"), nil, nil))
	actual, err := os.ReadFile(filepath.Join(primary, "@library/file.txt"))
	testutil.FailErr(t, "read primary result", err)
	if string(actual) != "promoted" {
		t.Fatalf("primary = %q", actual)
	}
	testutil.FailErr(t, "delete escaped primary path", promote.dropPrimary(task, "./@library/file.txt", nil))
	if _, err := os.Stat(filepath.Join(primary, "@library/file.txt")); !os.IsNotExist(err) {
		t.Fatalf("primary literal still exists: %v", err)
	}
	actual, err = os.ReadFile(filepath.Join(secondary, "file.txt"))
	testutil.FailErr(t, "read secondary result", err)
	if string(actual) != "secondary" {
		t.Fatalf("secondary changed: %q", actual)
	}
}

func TestPromoteRejectsPathsOutsideRoot(t *testing.T) {
	task := &api.WorkerTask{WorkspacePath: t.TempDir(), WorkspaceRoot: t.TempDir()}
	promote := PromoteRootsForTask(task, nil)
	for _, path := range []string{"../outside", filepath.Join(t.TempDir(), "outside"), "bad\x00name"} {
		if _, _, _, err := promote.ReadPairBytes(task, path); err == nil {
			t.Fatalf("accepted %q", path)
		}
	}
}

func TestPromoteRejectsUnknownSingleRootIdentity(t *testing.T) {
	primary, branch := t.TempDir(), t.TempDir()
	task := &api.WorkerTask{WorkspacePath: primary, WorkspaceRoot: branch, WorkspaceRootID: "primary"}
	promote := PromoteRootsForTask(task, []projectroot.RootRef{{ID: "primary", Path: primary, IsPrimary: true}})
	for _, id := range []string{"", "other"} {
		if _, _, _, err := promote.ReadPairBytesAt(task, id, "file.txt"); err == nil {
			t.Fatalf("accepted unknown root %q", id)
		}
	}
}
