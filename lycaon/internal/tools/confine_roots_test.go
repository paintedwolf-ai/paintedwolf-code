package tools

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
)

func rootsCtx(paths ...string) ToolContext {
	roots := make([]projectroot.RootRef, 0, len(paths))
	for i, p := range paths {
		roots = append(roots, projectroot.RootRef{
			ID:        "r" + string(rune('1'+i)),
			Label:     "l" + string(rune('1'+i)),
			Path:      p,
			IsPrimary: i == 0,
		})
	}
	return ToolContext{Roots: roots, ActiveRootID: "r1"}
}

func TestHostWriteRootPrefersWorkerBranch(t *testing.T) {
	tctx := rootsCtx("/proj/a", "/proj/b")
	if got := HostWriteRoot(tctx); got != "/proj/a" {
		t.Fatalf("HostWriteRoot without branch = %q, want the active root", got)
	}
	tctx.WorkerBranchRoot = "/branches/job1"
	if got := HostWriteRoot(tctx); got != "/branches/job1" {
		t.Fatalf("HostWriteRoot with branch = %q, want the worker branch", got)
	}
}

// A write worker can mutate its branch without an absolute write path back
// into the user's live roots.
func TestConfineRootsWorkerBranchIsExclusive(t *testing.T) {
	tctx := rootsCtx("/proj/a")
	tctx.WorkerBranchRoot = "/branches/job1"
	got := ConfineRootsForAction(tctx)
	if !slices.Equal(got, []string{"/branches/job1"}) {
		t.Fatalf("roots = %v, want only the worker branch", got)
	}
}

func TestWorkerConfineDeniesPrimarySourceReads(t *testing.T) {
	tctx := rootsCtx("/proj/a", "/proj/b")
	tctx.WorkerBranchRoot = "/proj/a/.paintedwolf/overlays/job1"
	tctx.WorkerSourceRoots = []string{"/proj/a", "/proj/b"}
	tctx.ReadRoots = []string{"/managed/skill"}
	inputs := ActionConfineInputsForContext(tctx, nil)
	if !slices.Equal(inputs.ReadDenyPaths, tctx.WorkerSourceRoots) {
		t.Fatalf("read deny paths = %v, want source roots %v", inputs.ReadDenyPaths, tctx.WorkerSourceRoots)
	}
	request := hitl.ActionConfineRequest(inputs)
	if !slices.Equal(request.ReadDenyPaths, tctx.WorkerSourceRoots) {
		t.Fatalf("request read deny paths = %v, want source roots %v", request.ReadDenyPaths, tctx.WorkerSourceRoots)
	}
	wantReadRoots := []string{"/managed/skill", tctx.WorkerBranchRoot}
	if !slices.Equal(inputs.ReadRoots, wantReadRoots) || !slices.Equal(request.ReadRoots, wantReadRoots) {
		t.Fatalf("read allow-backs inputs=%v request=%v want=%v", inputs.ReadRoots, request.ReadRoots, wantReadRoots)
	}
}

func TestConfineRootsIncludeEveryAttachedRoot(t *testing.T) {
	got := ConfineRootsForAction(rootsCtx("/proj/a", "/proj/b", "/proj/c"))
	for _, want := range []string{"/proj/a", "/proj/b", "/proj/c"} {
		if !slices.Contains(got, want) {
			t.Fatalf("roots = %v, want %q present", got, want)
		}
	}
}

// Model-supplied cwd symlinks cannot expand resolved write roots.
func TestConfineRootsUnaffectedByModelCwdSymlink(t *testing.T) {
	proj := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(proj, "docs-build")
	testutil.FailErr(t, "symlink", os.Symlink(outside, link))

	tctx := rootsCtx(proj)
	writeRoots := confine.WriteRootsForProject(tctx.ProjectID, ConfineRootsForAction(tctx))

	resolvedOutside, err := filepath.EvalSymlinks(outside)
	testutil.FailErr(t, "eval outside", err)
	for _, r := range writeRoots {
		if r == resolvedOutside {
			t.Fatalf("write roots %v contain the symlink target %q — a repo symlink widened the jail", writeRoots, resolvedOutside)
		}
	}

	// The union ignores tool arguments, so it is identical without the symlink.
	testutil.FailErr(t, "remove link", os.Remove(link))
	if after := confine.WriteRootsForProject(tctx.ProjectID, ConfineRootsForAction(tctx)); !slices.Equal(writeRoots, after) {
		t.Fatalf("write roots changed with the symlink removed: %v vs %v", writeRoots, after)
	}
}

// An in-project symlink that resolves outside the project (bazel-bin, pnpm store
// links) stays a usable cwd. Cwd containment is lexical; the union bounds writes.
func TestSymlinkedCwdStaysUsable(t *testing.T) {
	proj := t.TempDir()
	cache := t.TempDir()
	testutil.FailErr(t, "symlink", os.Symlink(cache, filepath.Join(proj, "bazel-bin")))

	tctx := rootsCtx(proj)
	roots := ConfineRootsForAction(tctx)
	if !slices.Contains(roots, proj) {
		t.Fatalf("roots = %v, want the project root", roots)
	}
	// The union does not grow. The active root appears as both host-selected and
	// attached; resolveWriteRoots collapses the duplicate.
	for _, r := range roots {
		if r != proj {
			t.Fatalf("roots = %v, want no path other than the project root; got %q", roots, r)
		}
	}
	if slices.Contains(confine.WriteRootsForProject("", roots), mustEval(t, cache)) {
		t.Fatalf("write roots must not contain the symlink target %q", cache)
	}
}

func mustEval(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	testutil.FailErr(t, "eval "+p, err)
	return r
}

// The gate's Contained stamp and the executor's DefaultConfinement both read the
// union, so one ToolContext yields one set.
func TestConfineRootsDeterministicForSameContext(t *testing.T) {
	tctx := rootsCtx("/proj/a", "/proj/b")
	tctx.WorkerBranchRoot = "/branches/job1"
	if a, b := ConfineRootsForAction(tctx), ConfineRootsForAction(tctx); !slices.Equal(a, b) {
		t.Fatalf("not deterministic: %v vs %v", a, b)
	}
}

func TestConfineRootsSkipsBlankPaths(t *testing.T) {
	tctx := rootsCtx("/proj/a", "   ")
	got := ConfineRootsForAction(tctx)
	for _, r := range got {
		if r == "" || r == "   " {
			t.Fatalf("roots = %v, want blanks dropped", got)
		}
	}
}

// Structurally unsafe roots are corrected rather than reported as human denials;
// the lane code rides as data.
func TestValidateRootsForActionPublishRegisteredCode(t *testing.T) {
	for name, reject := range map[string]*ToolReject{
		"attached": ValidateAttachedRootsForAction([]string{string(filepath.Separator)}),
		"granted":  ValidateGrantedRootsForAction([]string{string(filepath.Separator)}),
	} {
		if reject == nil || reject.Code != "SANDBOX_CAPABILITY_REQUEST_INVALID" {
			t.Fatalf("%s reject = %+v, want SANDBOX_CAPABILITY_REQUEST_INVALID", name, reject)
		}
		if reject.Data["reason"] != confine.WriteRootCodeFilesystemRoot {
			t.Fatalf("%s reject data = %+v, want reason %q", name, reject.Data, confine.WriteRootCodeFilesystemRoot)
		}
	}
}
