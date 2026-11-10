package toolexecution

import (
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
)

func rootsCtx(paths ...string) tools.ToolContext {
	roots := make([]projectroot.RootRef, 0, len(paths))
	for i, p := range paths {
		roots = append(roots, projectroot.RootRef{
			ID:        "r" + string(rune('1'+i)),
			Label:     "l" + string(rune('1'+i)),
			Path:      p,
			IsPrimary: i == 0,
		})
	}
	return tools.ToolContext{
		Source: tools.InvocationSource{Roots: roots,
			ActiveRootID: "r1"},
	}
}

func TestHostWriteRootPrefersWorkerBranch(t *testing.T) {
	tctx := rootsCtx("/proj/a", "/proj/b")
	if got := tools.HostWriteRoot(tctx); got != "/proj/a" {
		t.Fatalf("HostWriteRoot without branch = %q, want the active root", got)
	}
	tctx.Source.WorkerBranchRoot = "/branches/job1"
	if got := tools.HostWriteRoot(tctx); got != "/branches/job1" {
		t.Fatalf("HostWriteRoot with branch = %q, want the worker branch", got)
	}
}

// A write worker can mutate its branch without an absolute write path back
// into the user's live roots.
func TestConfineRootsWorkerBranchIsExclusive(t *testing.T) {
	tctx := rootsCtx("/proj/a")
	tctx.Source.WorkerBranchRoot = "/branches/job1"
	got := tools.ConfineRootsForAction(tctx)
	if !slices.Equal(got, []string{"/branches/job1"}) {
		t.Fatalf("roots = %v, want only the worker branch", got)
	}
}

// A read-scoped worker stays on the project tree but no worker's branch is its
// to read; an ordinary session excludes nothing.
// Several roots each own a directory in the branch, and each carries policy the
// floor and the native write review must recognize.
func TestConfineRootsNameEveryRootInAMultiRootBranch(t *testing.T) {
	tctx := rootsCtx("/proj/a", "/proj/b")
	tctx.Source.WorkerBranchRoot = "/branches/job1"
	got := tools.ConfineRootsForAction(tctx)
	if len(got) != 3 || got[0] != "/branches/job1" {
		t.Fatalf("roots = %v, want the branch and one directory per root", got)
	}
	for _, root := range got[1:] {
		if _, ok := confine.AgentPolicyPath(root+"/.paintedwolf/approvals.yaml", got...); !ok {
			t.Errorf("%s: project approvals not classified as agent policy", root)
		}
		if _, ok := confine.AgentPolicyPath(root+"/.agents/skills/x/SKILL.md", got...); !ok {
			t.Errorf("%s: project skill not classified as agent policy", root)
		}
	}
}

func TestReadWorkerConfineDeniesWorkerBranches(t *testing.T) {
	tctx := rootsCtx("/proj/a")
	if got := tools.ActionConfineInputsForContext(tctx, nil).ReadDenyPaths; len(got) != 0 {
		t.Fatalf("session read deny paths = %v, want none", got)
	}
	tctx.Identity.WorkerJobID = "job1"
	want := confine.WorkerReadDenyRoots(false)
	if got := tools.ActionConfineInputsForContext(tctx, nil).ReadDenyPaths; !slices.Equal(got, want) {
		t.Fatalf("read worker deny paths = %v, want %v", got, want)
	}
}

func TestWorkerConfineDeniesPrimarySourceReads(t *testing.T) {
	tctx := rootsCtx("/proj/a", "/proj/b")
	tctx.Source.WorkerBranchRoot = "/proj/a/.paintedwolf/overlays/job1"
	tctx.Source.WorkerSourceRoots = []string{"/proj/a", "/proj/b"}
	tctx.Files.ReadRoots = []string{"/managed/skill"}
	inputs := tools.ActionConfineInputsForContext(tctx, nil)
	wantDeny := append(confine.WorkerReadDenyRoots(true), tctx.Source.WorkerSourceRoots...)
	if !slices.Equal(inputs.ReadDenyPaths, wantDeny) {
		t.Fatalf("read deny paths = %v, want %v", inputs.ReadDenyPaths, wantDeny)
	}
	request := hitl.ActionConfineRequest(inputs)
	if !slices.Equal(request.ReadDenyPaths, wantDeny) {
		t.Fatalf("request read deny paths = %v, want %v", request.ReadDenyPaths, wantDeny)
	}
	wantReadRoots := []string{"/managed/skill", tctx.Source.WorkerBranchRoot}
	if !slices.Equal(inputs.ReadRoots, wantReadRoots) || !slices.Equal(request.ReadRoots, wantReadRoots) {
		t.Fatalf("read allow-backs inputs=%v request=%v want=%v", inputs.ReadRoots, request.ReadRoots, wantReadRoots)
	}
}

func TestConfineRootsIncludeEveryAttachedRoot(t *testing.T) {
	got := tools.ConfineRootsForAction(rootsCtx("/proj/a", "/proj/b", "/proj/c"))
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
	writeRoots, err := confine.WriteRootsForBoundary(tctx.Identity.ProjectID, tools.ConfineRootsForAction(tctx), nil, "")
	testutil.FailErr(t, "admit write roots", err)

	resolvedOutside, err := filepath.EvalSymlinks(outside)
	testutil.FailErr(t, "eval outside", err)
	for _, r := range writeRoots {
		if r == resolvedOutside {
			t.Fatalf("write roots %v contain the symlink target %q — a repo symlink widened the jail", writeRoots, resolvedOutside)
		}
	}

	// The union ignores tool arguments, so it is identical without the symlink.
	testutil.FailErr(t, "remove link", os.Remove(link))
	after, err := confine.WriteRootsForBoundary(tctx.Identity.ProjectID, tools.ConfineRootsForAction(tctx), nil, "")
	testutil.FailErr(t, "admit unchanged roots", err)
	if !slices.Equal(writeRoots, after) {
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
	roots := tools.ConfineRootsForAction(tctx)
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
	writeRoots, err := confine.WriteRootsForBoundary("", roots, nil, "")
	testutil.FailErr(t, "admit cwd roots", err)
	if slices.Contains(writeRoots, mustEval(t, cache)) {
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
	tctx.Source.WorkerBranchRoot = "/branches/job1"
	if a, b := tools.ConfineRootsForAction(tctx), tools.ConfineRootsForAction(tctx); !slices.Equal(a, b) {
		t.Fatalf("not deterministic: %v vs %v", a, b)
	}
}

func TestConfineRootsSkipsBlankPaths(t *testing.T) {
	tctx := rootsCtx("/proj/a", "   ")
	got := tools.ConfineRootsForAction(tctx)
	for _, r := range got {
		if r == "" || r == "   " {
			t.Fatalf("roots = %v, want blanks dropped", got)
		}
	}
}

// Structurally unsafe roots are corrected rather than reported as human denials;
// the lane code rides as data.
func TestValidateRootsForActionPublishRegisteredCode(t *testing.T) {
	for name, reject := range map[string]*toolrejection.ToolReject{
		"attached": tools.ValidateAttachedRootsForAction([]string{"relative"}),
		"granted":  tools.ValidateGrantedRootsForAction([]string{"relative"}),
	} {
		if reject == nil || reject.Code != "SANDBOX_CAPABILITY_REQUEST_INVALID" {
			t.Fatalf("%s reject = %+v, want SANDBOX_CAPABILITY_REQUEST_INVALID", name, reject)
		}
		if reject.Data["reason"] != confine.WriteRootCodeNotAbsolute {
			t.Fatalf("%s reject data = %+v, want reason %q", name, reject.Data, confine.WriteRootCodeNotAbsolute)
		}
	}
}

func TestValidateAttachedRootsForActionStillRefusesProtectedAndControlPlaneRoots(t *testing.T) {
	home := t.TempDir()
	cfgDir := filepath.Join(home, ".config", "paintedwolf")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("LYCAON_CONFIG_DIR", cfgDir)

	cases := []struct {
		name       string
		dir        string
		wantCode   string
		wantReason string
	}{
		{
			name:       "home",
			dir:        home,
			wantCode:   "CONTROL_PLANE_PATH_DENIED",
			wantReason: confine.WriteRootCodeContainsState,
		},
		{
			name:       "dot-config",
			dir:        filepath.Join(home, ".config"),
			wantCode:   "CONTROL_PLANE_PATH_DENIED",
			wantReason: confine.WriteRootCodeContainsState,
		},
		{
			name:       "dot-ssh",
			dir:        filepath.Join(home, ".ssh"),
			wantCode:   "SANDBOX_CAPABILITY_REQUEST_INVALID",
			wantReason: confine.WriteRootCodeSecretStore,
		},
		{
			name:       "configdir-root",
			dir:        configdir.Root(),
			wantCode:   "SANDBOX_CAPABILITY_REQUEST_INVALID",
			wantReason: confine.WriteRootCodeSecretStore,
		},
	}

	reg := project.NewMemoryRegistry()
	for _, tc := range cases {
		testutil.FailErr(t, "mkdir "+tc.name, os.MkdirAll(tc.dir, 0o700))
		p, err := project.CreateWithRoot(t.Context(), reg, tc.dir)
		if err != nil {
			t.Fatalf("%s: project registration should succeed, got %v", tc.name, err)
		}
		reject := tools.ValidateAttachedRootsForAction(project.RootPaths(p))
		if reject == nil {
			t.Fatalf("%s: expected ValidateAttachedRootsForAction to refuse %q", tc.name, tc.dir)
		}
		if reject.Code != tc.wantCode || reject.Data["reason"] != tc.wantReason {
			t.Fatalf("%s: reject = %+v, want code=%q reason=%q", tc.name, reject, tc.wantCode, tc.wantReason)
		}
	}
}
