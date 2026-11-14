package projectpaths_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

func canonicalPathsContext(t *testing.T) (tools.ToolContext, string, string) {
	t.Helper()
	base := t.TempDir()
	primary := filepath.Join(base, "lycaon")
	secondary := filepath.Join(base, "lycaon-den")
	testutil.FailErr(t, "mkdir primary", os.MkdirAll(filepath.Join(primary, "src"), 0o755))
	testutil.FailErr(t, "mkdir secondary", os.MkdirAll(filepath.Join(secondary, "src"), 0o755))
	roots := []projectroot.RootRef{
		{ID: "p", Label: "lycaon", Path: primary, IsPrimary: true},
		{ID: "d", Label: "lycaon-den", Path: secondary, IsPrimary: false},
	}
	tctx := tools.ToolContext{
		Source: tools.InvocationSource{Roots: roots, ActiveRootID: "d"},
		Identity: tools.InvocationIdentity{Agent: "implementer",
			SessionID: "test-session"},
	}
	return tctx, primary, secondary
}

// AC-WR07-01: CommandCwd returns the canonical display spelling for absolute,
// bare, and @label operands, keyed on the session's active root.
func TestCommandCwdCanonicalDisplay(t *testing.T) {
	tctx, primary, secondary := canonicalPathsContext(t)
	ctx := context.Background()
	cases := []struct {
		cwd         string
		wantAbs     string
		wantDisplay string
	}{
		{cwd: filepath.Join(primary, "src"), wantAbs: filepath.Join(primary, "src"), wantDisplay: "@lycaon/src"},
		{cwd: primary, wantAbs: primary, wantDisplay: "@lycaon"},
		{cwd: filepath.Join(secondary, "src"), wantAbs: filepath.Join(secondary, "src"), wantDisplay: "src"},
		{cwd: "src", wantAbs: filepath.Join(secondary, "src"), wantDisplay: "src"},
		{cwd: "@lycaon/src", wantAbs: filepath.Join(primary, "src"), wantDisplay: "@lycaon/src"},
		{cwd: "", wantAbs: secondary, wantDisplay: "."},
	}
	for _, tc := range cases {
		abs, display, err := projectpaths.CommandCwd(ctx, tctx, tc.cwd)
		testutil.FailErr(t, "CommandCwd "+tc.cwd, err)
		if abs != tc.wantAbs || display != tc.wantDisplay {
			t.Fatalf("CommandCwd(%q) = (%q, %q), want (%q, %q)", tc.cwd, abs, display, tc.wantAbs, tc.wantDisplay)
		}
	}
}

// Write workers accept in-root absolute cwd operands and map them into the
// worker branch; the display stays the canonical session spelling.
func TestCommandCwdWorkerBranchAcceptsInRootAbsolute(t *testing.T) {
	tctx, primary, secondary := canonicalPathsContext(t)
	branch := t.TempDir()
	primaryDir, err := projectroot.BranchDirForID("p")
	testutil.FailErr(t, "primary branch dir", err)
	secondaryDir, err := projectroot.BranchDirForID("d")
	testutil.FailErr(t, "secondary branch dir", err)
	testutil.FailErr(t, "mkdir branch dirs", os.MkdirAll(filepath.Join(branch, primaryDir, "src"), 0o755))
	testutil.FailErr(t, "mkdir branch secondary", os.MkdirAll(filepath.Join(branch, secondaryDir, "src"), 0o755))
	tctx.Source.WorkerBranchRoot = branch
	tctx.Source.BranchWorkspace = testutil.CompleteBranchWorkspace{}
	ctx := context.Background()

	abs, display, err := projectpaths.CommandCwd(ctx, tctx, filepath.Join(primary, "src"))
	testutil.FailErr(t, "CommandCwd absolute primary", err)
	if abs != filepath.Join(branch, primaryDir, "src") {
		t.Fatalf("abs = %q want branch-mapped %q", abs, filepath.Join(branch, primaryDir, "src"))
	}
	if display != "@lycaon/src" {
		t.Fatalf("display = %q want @lycaon/src", display)
	}

	abs, display, err = projectpaths.CommandCwd(ctx, tctx, filepath.Join(secondary, "src"))
	testutil.FailErr(t, "CommandCwd absolute secondary", err)
	if abs != filepath.Join(branch, secondaryDir, "src") {
		t.Fatalf("abs = %q want branch-mapped %q", abs, filepath.Join(branch, secondaryDir, "src"))
	}
	if display != "src" {
		t.Fatalf("display = %q want src (active root is secondary)", display)
	}

	// An absolute cwd outside every attached root stays out of scope.
	_, _, err = projectpaths.CommandCwd(ctx, tctx, filepath.Join(t.TempDir(), "elsewhere"))
	requireReject(t, err, "CWD_OUT_OF_SCOPE")
}

// Host spill paths resolve identically through the bare wire form and the
// @output namespace, and both display as @output/....
func TestResolveReadSpillOutputNamespaceRoundTrip(t *testing.T) {
	base := t.TempDir()
	ws := filepath.Join(base, "lycaon")
	host := filepath.Join(base, "host-data")
	rel := filepath.Join("tool-output", "a.txt")
	spill := filepath.Join(host, rel)
	testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Dir(spill), 0o700))
	testutil.FailErr(t, "write", os.WriteFile(spill, []byte("full"), 0o600))
	tctx := tools.ToolContext{
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Path: ws, IsPrimary: true}},
			ActiveRootID: "r1"},
		Host:     tools.InvocationHost{HostDataDir: host},
		Identity: tools.InvocationIdentity{Agent: "implementer"},
	}
	ctx := context.Background()

	bare, err := projectpaths.ResolveRead(ctx, nil, tctx, "tool-output/a.txt")
	testutil.FailErr(t, "ResolveRead bare spill", err)
	addressed, err := projectpaths.ResolveRead(ctx, nil, tctx, "@output/tool-output/a.txt")
	testutil.FailErr(t, "ResolveRead @output spill", err)
	if bare.Abs != addressed.Abs || bare.ScopeRel != addressed.ScopeRel {
		t.Fatalf("spill forms disagree: bare=%+v addressed=%+v", bare, addressed)
	}
	if bare.DisplayPath != "@output/tool-output/a.txt" || addressed.DisplayPath != "@output/tool-output/a.txt" {
		t.Fatalf("displays = %q / %q want @output/tool-output/a.txt", bare.DisplayPath, addressed.DisplayPath)
	}
	if bare.Root.ID != "host-data" || addressed.Root.ID != "host-data" {
		t.Fatalf("root ids = %q / %q want host-data", bare.Root.ID, addressed.Root.ID)
	}
	if !bare.Compressed || !addressed.Compressed {
		t.Fatal("blobstore spill lost its compressed classification")
	}
	// The @output namespace stays read-only.
	_, err = projectpaths.ResolveWrite(ctx, nil, tctx, "@output/tool-output/a.txt")
	requireReject(t, err, "SURVEY_PATH_ESCAPE")
}

func TestResolveReadPromptAttachmentNotToolOutput(t *testing.T) {
	base := t.TempDir()
	ws := filepath.Join(base, "lycaon")
	host := filepath.Join(base, "host-data")
	rel := filepath.Join("prompt-attachments", "abc", "notes.txt")
	abs := filepath.Join(host, rel)
	testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Dir(abs), 0o700))
	testutil.FailErr(t, "write", os.WriteFile(abs, []byte("x"), 0o600))
	tctx := tools.ToolContext{
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Path: ws, IsPrimary: true}},
			ActiveRootID: "r1"},
		Host:     tools.InvocationHost{HostDataDir: host},
		Identity: tools.InvocationIdentity{Agent: "implementer"},
	}
	resolved, err := projectpaths.ResolveRead(context.Background(), nil, tctx, "prompt-attachments/abc/notes.txt")
	testutil.FailErr(t, "ResolveRead prompt attachment", err)
	if resolved.DisplayPath != "@output/prompt-attachments/abc/notes.txt" {
		t.Fatalf("display = %q", resolved.DisplayPath)
	}
	if resolved.ToolOutput {
		t.Fatal("prompt attachments are not tool output")
	}
	if !resolved.Compressed {
		t.Fatal("prompt attachments are blobstore-managed")
	}
}
