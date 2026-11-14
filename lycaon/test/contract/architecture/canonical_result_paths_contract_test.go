package contract

import (
	"context"
	"encoding/json"
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
	"github.com/lycaon/lycaon/internal/tools/surveyreceipt"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// AC-WR07-01: canonical display paths in receipts, CommandCwd, outside-root
// grants, and host spill resolution, with active ≠ primary.
func TestCanonicalResultPathsReceiptsAndCommandCwd(t *testing.T) {
	fix := newMultiRootFixture(t)
	fix.activeID = "d"
	boundary := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true, RejectSymlinkEscape: true},
		[]sandbox.ToolProfile{{ID: "implement", Tools: map[string]bool{
			"stat": true, "list_dir": true, "find": true, "grep": true, "summarize": true,
		}}})
	tctx := fix.tctx("s1")
	ctx := context.Background()
	primaryFile := filepath.Join(fix.primaryDir, "primary-sentinel.txt")

	// stat receipts: absolute in-root, bare, and @label operands all render
	// the canonical session spelling.
	for _, tc := range []struct {
		name string
		arg  string
		want string
	}{
		{"absolute in-root", primaryFile, "@lycaon/primary-sentinel.txt"},
		{"bare active-root", "secondary-sentinel.txt", "secondary-sentinel.txt"},
		{"labeled other-root", "@lycaon/primary-sentinel.txt", "@lycaon/primary-sentinel.txt"},
	} {
		out, err := (&survey.StatTool{Boundary: boundary}).Run(ctx, map[string]any{"paths": []any{tc.arg}}, tctx)
		contractcheck.FailErr(t, "stat "+tc.name, err)
		receipt, ok := surveyreceipt.Parse(out)
		if !ok {
			t.Fatalf("stat %s: missing receipt in %q", tc.name, out)
		}
		if receipt.Path != tc.want {
			t.Fatalf("stat %s receipt path = %q want %q", tc.name, receipt.Path, tc.want)
		}
	}

	// find receipt keeps the canonical root spelling.
	findOut, err := (&survey.FindTool{Boundary: boundary}).Run(ctx,
		map[string]any{"path": "@lycaon", "name_glob": "*-sentinel.txt"}, tctx)
	contractcheck.FailErr(t, "find", err)
	findReceipt, ok := surveyreceipt.Parse(findOut)
	if !ok || findReceipt.Path != "@lycaon" {
		t.Fatalf("find receipt = %+v want path @lycaon", findReceipt)
	}

	// grep receipt keeps the canonical root spelling for a single target.
	grepOut, err := (&survey.GrepTool{Boundary: boundary}).Run(ctx,
		map[string]any{"pattern": "primary", "path": "@lycaon"}, tctx)
	contractcheck.FailErr(t, "grep", err)
	grepReceipt, ok := surveyreceipt.Parse(grepOut)
	if !ok || grepReceipt.Path != "@lycaon" {
		t.Fatalf("grep receipt = %+v want path @lycaon", grepReceipt)
	}

	// list_dir receipt keeps the canonical root spelling.
	listOut, err := (&survey.ListDirTool{Boundary: boundary}).Run(ctx, map[string]any{"path": "@lycaon"}, tctx)
	contractcheck.FailErr(t, "list_dir", err)
	listReceipt, ok := surveyreceipt.Parse(listOut)
	if !ok || listReceipt.Path != "@lycaon" {
		t.Fatalf("list_dir receipt = %+v want path @lycaon", listReceipt)
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

	// Approved outside-root directory listings render absolute paths and
	// drop zero entries.
	grant := t.TempDir()
	contractcheck.FailErr(t, "mkdir grant nested", os.MkdirAll(filepath.Join(grant, "nested"), 0o755))
	contractcheck.FailErr(t, "write grant file", os.WriteFile(filepath.Join(grant, "logo.txt"), []byte("x"), 0o644))
	contractcheck.FailErr(t, "write grant nested", os.WriteFile(filepath.Join(grant, "nested", "deep.txt"), []byte("x"), 0o644))
	release := projectpaths.SetGrantedAccessSource(func(_, _, probe string, _ bool) (projectpaths.Access, bool) {
		if probe == grant || strings.HasPrefix(probe, grant+string(filepath.Separator)) {
			return projectpaths.Access{Path: grant, Tree: true}, true
		}
		return projectpaths.Access{}, false
	})
	t.Cleanup(release)
	grantOut, err := (&survey.ListDirTool{Boundary: boundary}).Run(ctx, map[string]any{"path": grant, "max_depth": 3}, tctx)
	contractcheck.FailErr(t, "list_dir granted tree", err)
	var grantResp struct {
		Entries []struct {
			Path string `json:"path"`
		} `json:"entries"`
	}
	contractcheck.FailErr(t, "decode grant listing", json.Unmarshal([]byte(surveyContent(t, grantOut)), &grantResp))
	if len(grantResp.Entries) != 3 {
		t.Fatalf("grant listing entries = %d want 3: %+v", len(grantResp.Entries), grantResp.Entries)
	}
	for _, entry := range grantResp.Entries {
		if !filepath.IsAbs(entry.Path) {
			t.Fatalf("grant entry %q is not an absolute canonical path", entry.Path)
		}
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

func surveyContent(t *testing.T, out string) string {
	t.Helper()
	var wrap struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(out), &wrap); err == nil && wrap.Content != "" {
		return wrap.Content
	}
	return out
}
