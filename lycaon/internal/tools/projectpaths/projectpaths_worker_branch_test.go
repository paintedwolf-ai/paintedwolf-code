package projectpaths_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

func TestResolveWriteUnderMultiRootWorkerBranch(t *testing.T) {
	base := t.TempDir()
	primary := filepath.Join(base, "a")
	secondary := filepath.Join(base, "b")
	testutil.FailErr(t, "mkdir a", os.MkdirAll(filepath.Join(primary, "pkg"), 0o755))
	testutil.FailErr(t, "mkdir b", os.MkdirAll(filepath.Join(secondary, "pkg"), 0o755))
	branch := filepath.Join(base, "branch")
	primaryDir, err := projectroot.BranchDirForID("p")
	testutil.FailErr(t, "primary branch dir", err)
	secondaryDir, err := projectroot.BranchDirForID("s")
	testutil.FailErr(t, "secondary branch dir", err)
	testutil.FailErr(t, "mkdir branch", os.MkdirAll(filepath.Join(branch, primaryDir, "pkg"), 0o755))
	testutil.FailErr(t, "mkdir branch b", os.MkdirAll(filepath.Join(branch, secondaryDir, "pkg"), 0o755))
	rel := "pkg/x.go"
	testutil.FailErr(t, "write branch", os.WriteFile(filepath.Join(branch, secondaryDir, rel), []byte("v"), 0o644))

	roots := []projectroot.RootRef{
		{ID: "p", Label: "a", Path: primary, IsPrimary: true},
		{ID: "s", Label: "b", Path: secondary, IsPrimary: false},
	}
	tctx := tools.ToolContext{
		Roots:            roots,
		ActiveRootID:     "p",
		WorkerBranchRoot: branch,
		BranchWorkspace:  testutil.CompleteBranchWorkspace{},
		Agent:            "implementer",
	}
	res, err := projectpaths.ResolveWrite(context.Background(), nil, tctx, "@b/"+rel)
	testutil.FailErr(t, "ResolveWrite", err)
	want := filepath.Join(branch, secondaryDir, rel)
	if res.Abs != want {
		t.Fatalf("abs = %q want %q display=%q", res.Abs, want, res.DisplayPath)
	}
	if res.DisplayPath != "@b/"+rel {
		t.Fatalf("display = %q", res.DisplayPath)
	}
	if res.Root.Path != branch {
		t.Fatalf("root path = %q want branch %q", res.Root.Path, branch)
	}
}

func TestUnionDiscoveryRootsPrefersWorkerBranch(t *testing.T) {
	base := t.TempDir()
	primary := filepath.Join(base, "proj")
	branch := filepath.Join(base, "branch")
	testutil.FailErr(t, "mkdir primary", os.MkdirAll(primary, 0o755))
	testutil.FailErr(t, "mkdir branch", os.MkdirAll(branch, 0o755))
	tctx := tools.ToolContext{
		Roots:            []projectroot.RootRef{{ID: "p", Path: primary, IsPrimary: true}},
		ActiveRootID:     "p",
		WorkerBranchRoot: branch,
		BranchWorkspace:  testutil.CompleteBranchWorkspace{},
	}
	roots, err := projectpaths.UnionDiscoveryRoots(context.Background(), tctx, ".")
	testutil.FailErr(t, "UnionDiscoveryRoots", err)
	if len(roots) != 1 || roots[0].Path != branch {
		t.Fatalf("roots=%v want branch %q", roots, branch)
	}
}

func TestResolveReadAcceptsWorkerBranchAbsolutePath(t *testing.T) {
	base := t.TempDir()
	primary := filepath.Join(base, "primary")
	branch := filepath.Join(base, "branch")
	testutil.FailErr(t, "mkdir primary", os.MkdirAll(primary, 0o755))
	testutil.FailErr(t, "mkdir branch", os.MkdirAll(branch, 0o755))
	target := filepath.Join(branch, "pkg", "x.go")
	testutil.FailErr(t, "mkdir target", os.MkdirAll(filepath.Dir(target), 0o755))
	testutil.FailErr(t, "write target", os.WriteFile(target, []byte("package pkg\n"), 0o644))
	tctx := tools.ToolContext{
		Roots:            []projectroot.RootRef{{ID: "p", Path: primary, IsPrimary: true}},
		ActiveRootID:     "p",
		WorkerBranchRoot: branch,
		BranchWorkspace:  testutil.CompleteBranchWorkspace{},
	}
	resolved, err := projectpaths.ResolveRead(context.Background(), nil, tctx, target)
	testutil.FailErr(t, "ResolveRead", err)
	if resolved.Abs != target || resolved.ScopeRel != "pkg/x.go" {
		t.Fatalf("resolved = %+v", resolved)
	}
}

func TestResolveReadRejectsRelativeWorkerBranchEscapeWithoutBoundary(t *testing.T) {
	primary := t.TempDir()
	branch := t.TempDir()
	tctx := tools.ToolContext{
		Roots:            []projectroot.RootRef{{ID: "p", Path: primary, IsPrimary: true}},
		ActiveRootID:     "p",
		WorkerBranchRoot: branch,
		BranchWorkspace:  testutil.CompleteBranchWorkspace{},
	}
	_, err := projectpaths.ResolveRead(t.Context(), nil, tctx, "../outside")
	var reject *tools.ToolReject
	if !errors.As(err, &reject) || reject.Code != "SURVEY_PATH_ESCAPE" {
		t.Fatalf("relative worker escape = %v, want SURVEY_PATH_ESCAPE", err)
	}
}

func TestResolveReadDisplaysStableMultiRootBranchPathByLabel(t *testing.T) {
	base := t.TempDir()
	primary := filepath.Join(base, "primary")
	secondary := filepath.Join(base, "secondary")
	branch := filepath.Join(base, "branch")
	testutil.FailErr(t, "mkdir primary", os.MkdirAll(primary, 0o755))
	testutil.FailErr(t, "mkdir secondary", os.MkdirAll(secondary, 0o755))
	secondaryDir, err := projectroot.BranchDirForID("secondary-id")
	testutil.FailErr(t, "secondary branch directory", err)
	target := filepath.Join(branch, secondaryDir, "notes..draft.md")
	testutil.FailErr(t, "mkdir target", os.MkdirAll(filepath.Dir(target), 0o755))
	testutil.FailErr(t, "write target", os.WriteFile(target, []byte("draft\n"), 0o644))
	tctx := tools.ToolContext{
		Roots: []projectroot.RootRef{
			{ID: "primary-id", Label: "app", Path: primary, IsPrimary: true},
			{ID: "secondary-id", Label: "docs", Path: secondary},
		},
		ActiveRootID:     "primary-id",
		WorkerBranchRoot: branch,
		BranchWorkspace:  testutil.CompleteBranchWorkspace{},
	}
	resolved, err := projectpaths.ResolveRead(context.Background(), nil, tctx, target)
	testutil.FailErr(t, "resolve multi-root absolute path", err)
	if resolved.DisplayPath != "@docs/notes..draft.md" {
		t.Fatalf("display path = %q", resolved.DisplayPath)
	}
}

func TestResolveWriteWorkerWithoutBranchRejected(t *testing.T) {
	base := t.TempDir()
	testutil.FailErr(t, "mkdir pkg", os.MkdirAll(filepath.Join(base, "pkg"), 0o755))
	roots := []projectroot.RootRef{{ID: "p", Label: "a", Path: base, IsPrimary: true}}
	tctx := tools.ToolContext{
		Roots:        roots,
		ActiveRootID: "p",
		Agent:        "implementer",
		WorkerJobID:  "job-1",
	}

	_, err := projectpaths.ResolveWrite(context.Background(), nil, tctx, "pkg/x.go")
	var reject *tools.ToolReject
	if !errors.As(err, &reject) || reject.Code != "WORKER_WRITE_WITHOUT_BRANCH" {
		t.Fatalf("write without branch: got err %v, want WORKER_WRITE_WITHOUT_BRANCH reject", err)
	}

	if _, err := projectpaths.ResolveRead(context.Background(), nil, tctx, "pkg/x.go"); err != nil {
		t.Fatalf("ResolveRead without branch: %v", err)
	}
}
