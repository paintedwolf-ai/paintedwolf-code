package native

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/terminal"
	"github.com/lycaon/lycaon/internal/workspace"
)

func makeTerminalCwdScript(t *testing.T, root string) string {
	t.Helper()
	script := filepath.Join(root, "cwd.sh")
	body := "#!/bin/sh\nprintf 'CWD:%s\\n' \"$(cat marker)\"\nIFS= read -r _\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		testutil.FailErr(t, "write script", err)
	}
	return script
}

func writeMarker(t *testing.T, dir, value string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "marker"), []byte(value), 0o644); err != nil {
		testutil.FailErr(t, "write marker", err)
	}
}

func terminalCwdToolContext(roots []projectroot.RootRef, sessionID, workerJobID string) tools.ToolContext {
	return tools.ToolContext{
		SessionID:   sessionID,
		WorkerJobID: workerJobID,
		Agent:       "implement",
		Roots:       roots,
	}
}

func terminalSnapshotCwd(t *testing.T, openRaw string) string {
	t.Helper()
	var opened terminal.OpenResult
	testutil.FailErr(t, "unmarshal open", json.Unmarshal([]byte(openRaw), &opened))
	if opened.Snapshot == nil {
		t.Fatalf("open missing snapshot: %+v", opened)
	}
	for _, line := range opened.Snapshot.Lines {
		if idx := strings.Index(line, "CWD:"); idx >= 0 {
			return strings.TrimSpace(line[idx+4:])
		}
	}
	t.Fatalf("CWD: not found in snapshot lines: %v", opened.Snapshot.Lines)
	return ""
}

func TestTerminalOpenCwdSubdir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix openpty")
	}
	bg := newTestBackgroundRegistry(t)
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register", RegisterTerminalSessionTools(reg, bg))

	root := t.TempDir()
	pkg := filepath.Join(root, "pkg")
	testutil.FailErr(t, "mkdir pkg", os.MkdirAll(pkg, 0o755))
	writeMarker(t, root, "root")
	writeMarker(t, pkg, "pkg")
	script := makeTerminalCwdScript(t, root)

	tctx := terminalCwdToolContext([]projectroot.RootRef{{ID: "main", Path: root, IsPrimary: true}}, "sess", "")
	openRaw, err := reg.Run(context.Background(), terminal.OpenToolName, map[string]any{
		"command": script,
		"cwd":     "pkg",
	}, tctx)
	testutil.FailErr(t, "terminal_open", err)
	if got := terminalSnapshotCwd(t, openRaw); got != "pkg" {
		t.Fatalf("cwd marker = %q want %q", got, "pkg")
	}
}

func TestTerminalOpenCwdAtLabelRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix openpty")
	}
	bg := newTestBackgroundRegistry(t)
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register", RegisterTerminalSessionTools(reg, bg))

	root := t.TempDir()
	other := filepath.Join(root, "other")
	testutil.FailErr(t, "mkdir other", os.MkdirAll(other, 0o755))
	writeMarker(t, root, "root")
	writeMarker(t, other, "other")
	script := makeTerminalCwdScript(t, root)

	tctx := terminalCwdToolContext([]projectroot.RootRef{
		{ID: "main", Path: root, IsPrimary: true, Label: "main"},
		{ID: "other", Path: other, Label: "other"},
	}, "sess", "")
	openRaw, err := reg.Run(context.Background(), terminal.OpenToolName, map[string]any{
		"command": script,
		"cwd":     "@other",
	}, tctx)
	testutil.FailErr(t, "terminal_open", err)
	if got := terminalSnapshotCwd(t, openRaw); got != "other" {
		t.Fatalf("cwd marker = %q want %q", got, "other")
	}
}

func TestTerminalOpenCwdWorkerBranchResolvesArgUnderBranch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix openpty")
	}
	bg := newTestBackgroundRegistry(t)
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register", RegisterTerminalSessionTools(reg, bg))

	root := t.TempDir()
	pkg := filepath.Join(root, "pkg")
	branch := filepath.Join(root, "branch")
	branchPkg := filepath.Join(branch, "pkg")
	testutil.FailErr(t, "mkdir pkg", os.MkdirAll(pkg, 0o755))
	testutil.FailErr(t, "mkdir branch/pkg", os.MkdirAll(branchPkg, 0o755))
	writeMarker(t, root, "root")
	writeMarker(t, pkg, "primary-pkg")
	writeMarker(t, branch, "branch")
	writeMarker(t, branchPkg, "pkg")
	script := makeTerminalCwdScript(t, root)

	tctx := terminalCwdToolContext([]projectroot.RootRef{{ID: "main", Path: root, IsPrimary: true}}, "sess", "job")
	tctx.WorkerBranchRoot = branch
	tctx.BranchWorkspace = testutil.CompleteBranchWorkspace{}
	testutil.FailErr(t, "WriteJobMeta", workspace.WriteJobMeta(enginepaths.MetaDirForBranchRoot(branch), workspace.JobMeta{
		Roots: []workspace.JobMetaRoot{{ID: "root", Path: root, IsPrimary: true}},
	}))
	openRaw, err := reg.Run(context.Background(), terminal.OpenToolName, map[string]any{
		"command": script,
		"cwd":     "pkg",
	}, tctx)
	testutil.FailErr(t, "terminal_open", err)
	if got := terminalSnapshotCwd(t, openRaw); got != "pkg" {
		t.Fatalf("cwd marker = %q want branch/pkg marker %q", got, "pkg")
	}
}

func TestTerminalOpenCwdEmptyDefaultsToActiveRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix openpty")
	}
	bg := newTestBackgroundRegistry(t)
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register", RegisterTerminalSessionTools(reg, bg))

	root := t.TempDir()
	writeMarker(t, root, "root")
	script := makeTerminalCwdScript(t, root)

	tctx := terminalCwdToolContext([]projectroot.RootRef{{ID: "main", Path: root, IsPrimary: true}}, "sess", "")
	openRaw, err := reg.Run(context.Background(), terminal.OpenToolName, map[string]any{
		"command": script,
	}, tctx)
	testutil.FailErr(t, "terminal_open", err)
	if got := terminalSnapshotCwd(t, openRaw); got != "root" {
		t.Fatalf("cwd marker = %q want root %q", got, "root")
	}
}

func TestTerminalOpenCwdOutOfScope(t *testing.T) {
	bg := newTestBackgroundRegistry(t)
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register", RegisterTerminalSessionTools(reg, bg))

	root := t.TempDir()
	writeMarker(t, root, "root")
	script := makeTerminalCwdScript(t, root)

	tctx := terminalCwdToolContext([]projectroot.RootRef{{ID: "main", Path: root, IsPrimary: true}}, "sess", "")
	_, err := reg.Run(context.Background(), terminal.OpenToolName, map[string]any{
		"command": script,
		"cwd":     "/etc",
	}, tctx)
	if err == nil {
		t.Fatal("expected CWD_OUT_OF_SCOPE")
	}
	if !strings.Contains(err.Error(), "CWD_OUT_OF_SCOPE") {
		t.Fatalf("expected CWD_OUT_OF_SCOPE, got: %v", err)
	}
}

func TestTerminalOpenCwdNotDirectory(t *testing.T) {
	bg := newTestBackgroundRegistry(t)
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register", RegisterTerminalSessionTools(reg, bg))

	root := t.TempDir()
	file := filepath.Join(root, "README.md")
	testutil.FailErr(t, "write file", os.WriteFile(file, []byte("hi"), 0o644))
	writeMarker(t, root, "root")
	script := makeTerminalCwdScript(t, root)

	tctx := terminalCwdToolContext([]projectroot.RootRef{{ID: "main", Path: root, IsPrimary: true}}, "sess", "")
	_, err := reg.Run(context.Background(), terminal.OpenToolName, map[string]any{
		"command": script,
		"cwd":     "README.md",
	}, tctx)
	if err == nil {
		t.Fatal("expected CWD_NOT_DIRECTORY")
	}
	if !strings.Contains(err.Error(), "CWD_NOT_DIRECTORY") {
		t.Fatalf("expected CWD_NOT_DIRECTORY, got: %v", err)
	}
}
