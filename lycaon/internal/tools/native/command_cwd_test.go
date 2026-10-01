package native_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/workspace"
)

func newCommandCwdTool(t *testing.T) (*native.CommandTool, *bgprocess.Registry) {
	t.Helper()
	boundary := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true}, []sandbox.ToolProfile{
		{ID: "implement", Tools: map[string]bool{"command": true}},
	})
	reg := bgprocess.NewRegistry(bgprocess.DefaultConfig(), bgprocess.Hooks{})
	return &native.CommandTool{
		Runner:     hostcmd.NewRunner(),
		Boundary:   boundary,
		Background: reg,
	}, reg
}

func commandCwdToolContext(roots []projectroot.RootRef, sessionID, workerJobID string) tools.ToolContext {
	return tools.ToolContext{
		SessionID:   sessionID,
		WorkerJobID: workerJobID,
		Agent:       "implement",
		Roots:       roots,
	}
}

func writeCwdMarker(t *testing.T, dir, value string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "marker"), []byte(value), 0o644); err != nil {
		testutil.FailErr(t, "write marker", err)
	}
}

func TestCommandCwdPerCallSubdir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is unix-oriented")
	}
	tool, _ := newCommandCwdTool(t)
	root := t.TempDir()
	pkg := filepath.Join(root, "pkg")
	testutil.FailErr(t, "mkdir pkg", os.MkdirAll(pkg, 0o755))
	writeCwdMarker(t, root, "root")
	writeCwdMarker(t, pkg, "pkg")

	out, err := tool.Run(context.Background(), map[string]any{
		"command": "cat marker",
		"cwd":     "pkg",
	}, commandCwdToolContext([]projectroot.RootRef{{ID: "primary", Path: root, IsPrimary: true}}, "sess", ""))
	testutil.FailErr(t, "command cwd=pkg", err)
	if !strings.Contains(out, "pkg") {
		t.Fatalf("output = %q want marker 'pkg'", out)
	}
}

func TestCommandCwdAtLabelRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is unix-oriented")
	}
	tool, _ := newCommandCwdTool(t)
	root := t.TempDir()
	other := filepath.Join(root, "other")
	testutil.FailErr(t, "mkdir other", os.MkdirAll(other, 0o755))
	writeCwdMarker(t, root, "root")
	writeCwdMarker(t, other, "other")

	out, err := tool.Run(context.Background(), map[string]any{
		"command": "cat marker",
		"cwd":     "@other",
	}, commandCwdToolContext([]projectroot.RootRef{
		{ID: "primary", Path: root, IsPrimary: true, Label: "main"},
		{ID: "other", Path: other, Label: "other"},
	}, "sess", ""))
	testutil.FailErr(t, "command cwd=@other", err)
	if !strings.Contains(out, "other") {
		t.Fatalf("output = %q want marker 'other'", out)
	}
}

func TestCommandCwdAtLabelSubPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is unix-oriented")
	}
	tool, _ := newCommandCwdTool(t)
	root := t.TempDir()
	other := filepath.Join(root, "other")
	sub := filepath.Join(other, "sub")
	testutil.FailErr(t, "mkdir sub", os.MkdirAll(sub, 0o755))
	writeCwdMarker(t, root, "root")
	writeCwdMarker(t, other, "other")
	writeCwdMarker(t, sub, "sub")

	out, err := tool.Run(context.Background(), map[string]any{
		"command": "cat marker",
		"cwd":     "@other/sub",
	}, commandCwdToolContext([]projectroot.RootRef{
		{ID: "primary", Path: root, IsPrimary: true, Label: "main"},
		{ID: "other", Path: other, Label: "other"},
	}, "sess", ""))
	testutil.FailErr(t, "command cwd=@other/sub", err)
	if !strings.Contains(out, "sub") {
		t.Fatalf("output = %q want marker 'sub'", out)
	}
}

func TestCommandCwdWorkerBranchResolvesArgUnderBranch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is unix-oriented")
	}
	tool, _ := newCommandCwdTool(t)
	root := t.TempDir()
	pkg := filepath.Join(root, "pkg")
	branch := filepath.Join(root, "branch")
	branchPkg := filepath.Join(branch, "pkg")
	testutil.FailErr(t, "mkdir pkg", os.MkdirAll(pkg, 0o755))
	testutil.FailErr(t, "mkdir branch/pkg", os.MkdirAll(branchPkg, 0o755))
	writeCwdMarker(t, root, "root")
	writeCwdMarker(t, pkg, "primary-pkg")
	writeCwdMarker(t, branch, "branch")
	writeCwdMarker(t, branchPkg, "pkg")

	tctx := commandCwdToolContext([]projectroot.RootRef{{ID: "primary", Path: root, IsPrimary: true}}, "sess", "job")
	tctx.WorkerBranchRoot = branch
	tctx.BranchWorkspace = testutil.CompleteBranchWorkspace{}
	testutil.FailErr(t, "WriteJobMeta", workspace.WriteJobMeta(enginepaths.MetaDirForBranchRoot(branch), workspace.JobMeta{
		Roots: []workspace.JobMetaRoot{{ID: "root", Path: root, IsPrimary: true}},
	}))

	out, err := tool.Run(context.Background(), map[string]any{
		"command": "cat marker",
		"cwd":     "pkg",
	}, tctx)
	testutil.FailErr(t, "command worker-branch cwd=pkg", err)
	if !strings.Contains(out, "pkg") {
		t.Fatalf("output = %q want marker 'pkg' under branch", out)
	}
	if strings.Contains(out, "primary-pkg") {
		t.Fatalf("worker cwd must not resolve onto primary tree; got %q", out)
	}
	if !strings.Contains(out, `"cwd":"pkg"`) {
		t.Fatalf("result should echo effective cwd; got %q", out)
	}
}

func TestCommandCwdWorkerBranchEmptyDefaultsToBranchRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is unix-oriented")
	}
	tool, _ := newCommandCwdTool(t)
	root := t.TempDir()
	branch := filepath.Join(root, "branch")
	testutil.FailErr(t, "mkdir branch", os.MkdirAll(branch, 0o755))
	writeCwdMarker(t, root, "root")
	writeCwdMarker(t, branch, "branch")

	tctx := commandCwdToolContext([]projectroot.RootRef{{ID: "primary", Path: root, IsPrimary: true}}, "sess", "job")
	tctx.WorkerBranchRoot = branch
	tctx.BranchWorkspace = testutil.CompleteBranchWorkspace{}
	testutil.FailErr(t, "WriteJobMeta", workspace.WriteJobMeta(enginepaths.MetaDirForBranchRoot(branch), workspace.JobMeta{
		Roots: []workspace.JobMetaRoot{{ID: "root", Path: root, IsPrimary: true}},
	}))

	out, err := tool.Run(context.Background(), map[string]any{
		"command": "cat marker",
	}, tctx)
	testutil.FailErr(t, "command worker-branch no cwd", err)
	if !strings.Contains(out, "branch") {
		t.Fatalf("output = %q want marker 'branch'", out)
	}
}

func TestCommandCwdWorkerBranchOutOfScope(t *testing.T) {
	tool, _ := newCommandCwdTool(t)
	root := t.TempDir()
	branch := filepath.Join(root, "branch")
	testutil.FailErr(t, "mkdir branch", os.MkdirAll(branch, 0o755))

	tctx := commandCwdToolContext([]projectroot.RootRef{{ID: "primary", Path: root, IsPrimary: true}}, "sess", "job")
	tctx.WorkerBranchRoot = branch
	tctx.BranchWorkspace = testutil.CompleteBranchWorkspace{}
	testutil.FailErr(t, "WriteJobMeta", workspace.WriteJobMeta(enginepaths.MetaDirForBranchRoot(branch), workspace.JobMeta{
		Roots: []workspace.JobMetaRoot{{ID: "root", Path: root, IsPrimary: true}},
	}))

	_, err := tool.Run(context.Background(), map[string]any{
		"command": "cat marker",
		"cwd":     "/etc",
	}, tctx)
	if err == nil {
		t.Fatal("expected CWD_OUT_OF_SCOPE")
	}
	if !strings.Contains(err.Error(), "CWD_OUT_OF_SCOPE") {
		t.Fatalf("expected CWD_OUT_OF_SCOPE, got: %v", err)
	}
}

func TestCommandCwdEmptyDefaultsToActiveRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is unix-oriented")
	}
	tool, _ := newCommandCwdTool(t)
	root := t.TempDir()
	writeCwdMarker(t, root, "root")

	out, err := tool.Run(context.Background(), map[string]any{
		"command": "cat marker",
	}, commandCwdToolContext([]projectroot.RootRef{{ID: "primary", Path: root, IsPrimary: true}}, "sess", ""))
	testutil.FailErr(t, "command no cwd", err)
	if !strings.Contains(out, "root") {
		t.Fatalf("output = %q want marker 'root'", out)
	}
}

func TestCommandCwdOutOfScope(t *testing.T) {
	tool, _ := newCommandCwdTool(t)
	root := t.TempDir()

	_, err := tool.Run(context.Background(), map[string]any{
		"command": "cat marker",
		"cwd":     "/etc",
	}, commandCwdToolContext([]projectroot.RootRef{{ID: "primary", Path: root, IsPrimary: true}}, "sess", ""))
	if err == nil {
		t.Fatal("expected CWD_OUT_OF_SCOPE")
	}
	if !strings.Contains(err.Error(), "CWD_OUT_OF_SCOPE") {
		t.Fatalf("expected CWD_OUT_OF_SCOPE, got: %v", err)
	}
}

func TestCommandCwdUnknownLabelOutOfScope(t *testing.T) {
	tool, _ := newCommandCwdTool(t)
	root := t.TempDir()

	_, err := tool.Run(context.Background(), map[string]any{
		"command": "cat marker",
		"cwd":     "@nope",
	}, commandCwdToolContext([]projectroot.RootRef{{ID: "primary", Path: root, IsPrimary: true}}, "sess", ""))
	if err == nil {
		t.Fatal("expected CWD_OUT_OF_SCOPE")
	}
	if !strings.Contains(err.Error(), "CWD_OUT_OF_SCOPE") {
		t.Fatalf("expected CWD_OUT_OF_SCOPE, got: %v", err)
	}
}

func TestCommandCwdNotDirectory(t *testing.T) {
	tool, _ := newCommandCwdTool(t)
	root := t.TempDir()
	file := filepath.Join(root, "README.md")
	testutil.FailErr(t, "write file", os.WriteFile(file, []byte("hi"), 0o644))

	_, err := tool.Run(context.Background(), map[string]any{
		"command": "cat marker",
		"cwd":     "README.md",
	}, commandCwdToolContext([]projectroot.RootRef{{ID: "primary", Path: root, IsPrimary: true}}, "sess", ""))
	if err == nil {
		t.Fatal("expected CWD_NOT_DIRECTORY")
	}
	if !strings.Contains(err.Error(), "CWD_NOT_DIRECTORY") {
		t.Fatalf("expected CWD_NOT_DIRECTORY, got: %v", err)
	}
}

func TestCommandPerCallEnvStillWorks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("printenv fixture is unix-oriented")
	}
	tool, _ := newCommandCwdTool(t)
	root := t.TempDir()

	out, err := tool.Run(context.Background(), map[string]any{
		"command": "printenv FOO",
		"env":     map[string]any{"FOO": "bar"},
	}, commandCwdToolContext([]projectroot.RootRef{{ID: "primary", Path: root, IsPrimary: true}}, "sess", ""))
	testutil.FailErr(t, "command printenv", err)
	if !strings.Contains(out, "bar") {
		t.Fatalf("expected per-call env FOO=bar, got: %s", out)
	}
}
