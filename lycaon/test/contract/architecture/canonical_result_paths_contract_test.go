package contract

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/summarize"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/survey"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// AC-WR07-01: canonical display paths in receipts, CommandCwd, outside-root
// grants, and host spill resolution, with active ≠ primary.
func TestCanonicalResultPathsReceiptsAndCommandCwd(t *testing.T) {
	fix := newMultiRootFixture(t)
	fix.activeID = "d"
	boundary := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true, RejectSymlinkEscape: true},
		[]sandbox.ToolProfile{{ID: "implement", Tools: map[string]bool{"summarize": true}}})
	tctx := fix.tctx("s1")
	ctx := context.Background()
	primaryFile := filepath.Join(fix.primaryDir, "primary-sentinel.txt")

	// Absolute in-root, bare, and @label operands all resolve to the canonical
	// session spelling.
	for _, tc := range []struct {
		name string
		arg  string
		want string
	}{
		{"absolute in-root", primaryFile, "@lycaon/primary-sentinel.txt"},
		{"bare active-root", "secondary-sentinel.txt", "secondary-sentinel.txt"},
		{"labeled other-root", "@lycaon/primary-sentinel.txt", "@lycaon/primary-sentinel.txt"},
	} {
		resolved, err := projectpaths.ResolveRead(ctx, boundary, tctx, tc.arg)
		contractcheck.FailErr(t, "resolve "+tc.name, err)
		if resolved.DisplayPath != tc.want {
			t.Fatalf("%s display = %q want %q", tc.name, resolved.DisplayPath, tc.want)
		}
	}

	// summarize SUMMARIZE_NO_MATERIAL receipt renders canonical paths.
	_, summarizeErr := (&survey.SummarizeTool{Boundary: boundary, Caps: summarize.DefaultCaps()}).Run(ctx,
		map[string]any{"path": "@lycaon/primary-sentinel.txt", "pattern": `func handle[A-Z]`}, tctx)
	var reject *toolrejection.ToolReject
	if !errors.As(summarizeErr, &reject) || reject.Code != "SUMMARIZE_NO_MATERIAL" {
		t.Fatalf("summarize err = %v, want SUMMARIZE_NO_MATERIAL", summarizeErr)
	}
	if reject.Data["path"] != "@lycaon/primary-sentinel.txt" {
		t.Fatalf("summarize receipt path = %v want @lycaon/primary-sentinel.txt", reject.Data["path"])
	}

	// CommandCwd returns canonical displays for every operand spelling.
	cwdCases := []struct {
		cwd         string
		wantDisplay string
	}{
		{fix.primaryDir, "@lycaon"},
		{fix.secondaryDir, "."},
		{"@lycaon", "@lycaon"},
		{".", "."},
	}
	for _, tc := range cwdCases {
		_, display, err := projectpaths.CommandCwd(ctx, tctx, tc.cwd)
		contractcheck.FailErr(t, "CommandCwd "+tc.cwd, err)
		if display != tc.wantDisplay {
			t.Fatalf("CommandCwd(%q) display = %q want %q", tc.cwd, display, tc.wantDisplay)
		}
	}

	// Write-worker CommandCwd accepts an in-root absolute path and maps it
	// into the worker branch.
	branch := t.TempDir()
	primaryDir, err := projectroot.BranchDirForID("p")
	contractcheck.FailErr(t, "primary branch dir", err)
	secondaryDir, err := projectroot.BranchDirForID("d")
	contractcheck.FailErr(t, "secondary branch dir", err)
	contractcheck.FailErr(t, "mkdir fixture src dirs", os.MkdirAll(filepath.Join(fix.primaryDir, "src"), 0o755))
	contractcheck.FailErr(t, "mkdir fixture secondary src", os.MkdirAll(filepath.Join(fix.secondaryDir, "src"), 0o755))
	contractcheck.FailErr(t, "mkdir branch dirs", os.MkdirAll(filepath.Join(branch, primaryDir, "src"), 0o755))
	contractcheck.FailErr(t, "mkdir branch secondary", os.MkdirAll(filepath.Join(branch, secondaryDir, "src"), 0o755))
	worker := tctx
	worker.Source.WorkerBranchRoot = branch
	worker.Source.BranchWorkspace = testutil.CompleteBranchWorkspace{}
	abs, display, err := projectpaths.CommandCwd(ctx, worker, filepath.Join(fix.primaryDir, "src"))
	contractcheck.FailErr(t, "worker CommandCwd absolute", err)
	if abs != filepath.Join(branch, primaryDir, "src") {
		t.Fatalf("worker CommandCwd abs = %q want branch-mapped %q", abs, filepath.Join(branch, primaryDir, "src"))
	}
	if display != "@lycaon/src" {
		t.Fatalf("worker CommandCwd display = %q want @lycaon/src", display)
	}
	_, _, err = projectpaths.CommandCwd(ctx, worker, filepath.Join(fix.secondaryDir, "src"))
	contractcheck.FailErr(t, "worker CommandCwd secondary absolute", err)
	_ = secondaryDir

	// Approved outside-root paths render as absolute paths.
	grant := t.TempDir()
	contractcheck.FailErr(t, "mkdir grant nested", os.MkdirAll(filepath.Join(grant, "nested"), 0o755))
	contractcheck.FailErr(t, "write grant nested", os.WriteFile(filepath.Join(grant, "nested", "deep.txt"), []byte("x"), 0o644))
	release := projectpaths.SetGrantedAccessSource(func(_, _, probe string, _ bool) (projectpaths.Access, bool) {
		if probe == grant || strings.HasPrefix(probe, grant+string(filepath.Separator)) {
			return projectpaths.Access{Path: grant, Tree: true}, true
		}
		return projectpaths.Access{}, false
	})
	t.Cleanup(release)
	granted, err := projectpaths.ResolveRead(ctx, boundary, tctx, filepath.Join(grant, "nested", "deep.txt"))
	contractcheck.FailErr(t, "resolve granted path", err)
	if !filepath.IsAbs(granted.DisplayPath) {
		t.Fatalf("granted display %q is not an absolute canonical path", granted.DisplayPath)
	}

	// Host spill paths resolve through @output without shadowing bare
	// session-folder paths.
	host := t.TempDir()
	spillRel := "tool-output/contract-spill.txt"
	spillAbs := filepath.Join(host, filepath.FromSlash(spillRel))
	contractcheck.FailErr(t, "mkdir spill", os.MkdirAll(filepath.Dir(spillAbs), 0o700))
	contractcheck.FailErr(t, "write spill", os.WriteFile(spillAbs, []byte("spill"), 0o600))
	// Decoy: a same-named file at the host data root must not capture the
	// bare session-folder spelling.
	contractcheck.FailErr(t, "write host decoy", os.WriteFile(filepath.Join(host, "notes.txt"), []byte("decoy"), 0o600))
	spillTctx := tctx
	spillTctx.Host = tools.InvocationHost{HostDataDir: host, SessionOutputDir: filepath.Join(t.TempDir(), "output")}
	bare, err := projectpaths.ResolveRead(ctx, nil, spillTctx, spillRel)
	contractcheck.FailErr(t, "ResolveRead bare spill", err)
	addressed, err := projectpaths.ResolveRead(ctx, nil, spillTctx, "@output/"+spillRel)
	contractcheck.FailErr(t, "ResolveRead @output spill", err)
	if bare.Abs != addressed.Abs || bare.DisplayPath != addressed.DisplayPath {
		t.Fatalf("spill forms disagree: bare=%+v addressed=%+v", bare, addressed)
	}
	if bare.DisplayPath != "@output/"+spillRel {
		t.Fatalf("spill display = %q want @output/%s", bare.DisplayPath, spillRel)
	}
	// A bare session-folder path still resolves against the session folder
	// roots, not the spill namespace.
	sessionFile := filepath.Join(fix.secondaryDir, "notes.txt")
	contractcheck.FailErr(t, "write session notes", os.WriteFile(sessionFile, []byte("notes"), 0o600))
	sessionResolved, err := projectpaths.ResolveRead(ctx, nil, spillTctx, "notes.txt")
	contractcheck.FailErr(t, "ResolveRead session file", err)
	if sessionResolved.Abs != sessionFile {
		t.Fatalf("session resolution = %q want %q (spill namespace shadowed a bare path)", sessionResolved.Abs, sessionFile)
	}
}
