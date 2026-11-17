package contract

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/test/contract/internal/catalogfixture"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// One write-root union decides the applied profile's write verdicts and the
// gate's write authority: paths under a granted root or the session scratch
// root are writable and run without a card, a path outside every root is
// refused by the profile and gated.
func TestInvariantWriteRootSSOTSharedByGateAndConfine(t *testing.T) {
	confine.TestingSetAutoConfine(t)
	t.Setenv("LYCAON_BYPASS_APPROVALS", "")
	t.Setenv("LYCAON_SANDBOX", "")
	t.Setenv("LYCAON_SANDBOX_NETWORK", "")
	requireNonHTTPConfinement(t)
	proj, granted, scratch := t.TempDir(), t.TempDir(), t.TempDir()
	writeRoots, err := confine.WriteRootsForBoundary("", []string{proj}, []string{granted}, scratch)
	contractcheck.FailErr(t, "admit write roots", err)
	conf, ok := confine.DefaultConfinement(confine.Request{
		Roots: []string{proj}, GrantedWriteRoots: []string{granted}, SessionScratchRoot: scratch,
	})
	if !ok || conf == nil {
		t.Fatal("no confinement for the project root")
	}
	contained := hitl.ContainedFromConfinement(conf, ok)
	if !slices.Equal(contained.WriteRoots, writeRoots) {
		t.Fatalf("gate write roots %v differ from the boundary union %v", contained.WriteRoots, writeRoots)
	}
	boundary := confine.BoundaryOf(conf)
	if !boundary.Applied {
		t.Fatal("the confinement projects no applied boundary")
	}
	rules := boundary.Filesystem
	for path, inside := range map[string]bool{
		filepath.Join(granted, "out.txt"):      true,
		filepath.Join(scratch, "out.txt"):      true,
		"/etc/lycaon-write-root-probe/out.txt": false,
	} {
		if got := confine.PathWithinRoots(path, writeRoots); got != inside {
			t.Errorf("%s within write roots = %v, want %v", path, got, inside)
		}
		if got := rules.Verdict(confine.AccessWrite, path); got.Allowed != inside {
			t.Errorf("%s profile write verdict = %+v, want allowed=%v", path, got, inside)
		}
		res := evalBundled(t, hitl.ProposedAction{
			Invocation: hitl.ActionInvocation{Tool: "write", Files: []string{path}},
			Scope:      hitl.ActionScope{ProjectDir: proj, SessionScratchRoot: scratch},
			Execution:  hitl.ActionExecution{Contained: contained},
		})
		if res.AutoApproved() != inside {
			t.Errorf("%s gate auto-approved = %v, want %v: %+v", path, res.AutoApproved(), inside, res)
		}
	}
}

// Active sandbox reject codes must exist in the catalog.
func TestInvariantSandboxHintCatalogPresent(t *testing.T) {
	t.Parallel()
	for _, outcome := range isolation.Outcomes() {
		catalogfixture.FindStockPolicyFile(t, outcome.Code)
	}
	for _, code := range []string{"VERIFY_UNVERIFIABLE", "WEB_SEARCH_HOST_DENIED"} {
		catalogfixture.FindStockPolicyFile(t, code)
	}
}
