package worker_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestPromoteRootsMapsNonPrimaryRoot(t *testing.T) {
	base := t.TempDir()
	primary := filepath.Join(base, "a")
	secondary := filepath.Join(base, "b")
	for _, dir := range []string{primary, secondary} {
		testutil.FailErr(t, "mkdir", os.MkdirAll(dir, 0o755))
	}
	rel := "pkg/x.go"
	testutil.FailErr(t, "mkdir pkg", os.MkdirAll(filepath.Join(primary, "pkg"), 0o755))
	testutil.FailErr(t, "mkdir pkg b", os.MkdirAll(filepath.Join(secondary, "pkg"), 0o755))
	testutil.FailErr(t, "write primary", os.WriteFile(filepath.Join(primary, rel), []byte("primary\n"), 0o644))
	testutil.FailErr(t, "write branch", os.WriteFile(filepath.Join(secondary, rel), []byte("primary\n"), 0o644))
	branchRoot := filepath.Join(base, "branch")
	primaryDir, err := projectroot.BranchDirForID("p")
	testutil.FailErr(t, "primary branch dir", err)
	secondaryDir, err := projectroot.BranchDirForID("s")
	testutil.FailErr(t, "secondary branch dir", err)
	testutil.FailErr(t, "mkdir branch a", os.MkdirAll(filepath.Join(branchRoot, primaryDir, "pkg"), 0o755))
	testutil.FailErr(t, "mkdir branch b", os.MkdirAll(filepath.Join(branchRoot, secondaryDir, "pkg"), 0o755))
	testutil.FailErr(t, "write overlay", os.WriteFile(filepath.Join(branchRoot, secondaryDir, rel), []byte("branch\n"), 0o644))

	roots := []projectroot.RootRef{
		{ID: "p", Label: "a", Path: primary, IsPrimary: true},
		{ID: "s", Label: "b", Path: secondary, IsPrimary: false},
	}
	task := &api.WorkerTask{
		ProjectID:       testdbseed.DefaultProjectID,
		WorkspacePath:   primary,
		WorkspaceRoot:   branchRoot,
		WorkspaceRootID: "p",
	}
	promote := worker.PromoteRootsForTask(task, roots)
	primaryBytes, branchBytes, present, err := promote.ReadPairBytes(task, "@b/"+rel)
	testutil.FailErr(t, "ReadPair", err)
	if !present || string(primaryBytes) != "primary\n" || string(branchBytes) != "branch\n" {
		t.Fatalf("pair = (%q, %q, %v)", primaryBytes, branchBytes, present)
	}
	testutil.FailErr(t, "WritePrimaryText", promote.WritePrimaryText(task, "@b/"+rel, "merged\n", textfile.UTF8))
	data, err := os.ReadFile(filepath.Join(secondary, rel))
	testutil.FailErr(t, "read promoted", err)
	if string(data) != "merged\n" {
		t.Fatalf("promoted = %q", data)
	}
}

func TestBranchRelForMultiRootSandbox(t *testing.T) {
	roots := []projectroot.RootRef{
		{ID: "p", Label: "a", Path: "/a", IsPrimary: true},
		{ID: "s", Label: "b", Path: "/b", IsPrimary: false},
	}
	promote := worker.PromoteRootsForTask(&api.WorkerTask{WorkspaceRoot: "/branch"}, roots)
	branchRel, display, err := promote.BranchRel("p", "@b/pkg/x.go")
	testutil.FailErr(t, "BranchRel", err)
	secondaryDir, err := projectroot.BranchDirForID("s")
	testutil.FailErr(t, "secondary branch dir", err)
	if branchRel != filepath.ToSlash(filepath.Join(secondaryDir, "pkg/x.go")) || display != "@b/pkg/x.go" {
		t.Fatalf("branchRel=%q display=%q", branchRel, display)
	}
}
