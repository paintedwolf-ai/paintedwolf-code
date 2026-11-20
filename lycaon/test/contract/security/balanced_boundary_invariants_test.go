package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/test/contract/internal/catalogfixture"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestInvariantWriteRootSSOTSharedByGateAndConfine(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", "/opt/paintedwolf-fixture-state")
	proj, scratch := t.TempDir(), t.TempDir()
	writeRoots, err := confine.WriteRootsForBoundary("", []string{proj}, nil, scratch)
	contractcheck.FailErr(t, "admit write roots", err)
	if len(writeRoots) == 0 {
		t.Fatal("WriteRootsForBoundary returned no roots")
	}

	for _, root := range []string{proj, scratch} {
		if !confine.PathWithinRoots(filepath.Join(root, "result"), writeRoots) {
			t.Fatalf("expected %q to admit writes: %v", root, writeRoots)
		}
	}
	for _, outside := range []string{"/tmp/scratch", "/var/tmp/result", "/etc/lycaon-probe"} {
		if confine.PathWithinRoots(outside, writeRoots) {
			t.Fatalf("unexpected write authority for %q: %v", outside, writeRoots)
		}
	}
}

// Contained is stamped from DefaultConfinement onto ProposedAction.
func TestInvariantProductionGateWiresContainment(t *testing.T) {
	t.Parallel()
	policyPath := filepath.Join(contractcheck.RepoRoot(t), "lycaon", "internal", "toolexecution", "approval_policy.go")
	policyRaw, err := os.ReadFile(policyPath)
	contractcheck.FailErr(t, "read approval_policy.go", err)
	policySrc := string(policyRaw)
	// The stamp reads the executor's own confine.Request rather than rebuilding one
	// field by field, so the guard pins that identity plus the roots backfill — not a
	// field order, which would fail on an unrelated field being added.
	if !strings.Contains(policySrc, "hitl.ContainedForRequest(confReq)") {
		t.Fatal("approval_policy.go must stamp Contained via hitl.ContainedForRequest")
	}
	if !strings.Contains(policySrc, "confReq := eval.ConfineRequest") {
		t.Fatal("Contained must be stamped from the same confine.Request the executor applies, not a rebuilt one")
	}
	if !strings.Contains(policySrc, "confReq.Roots = []string{eval.ProjectDir}") {
		t.Fatal("Contained must carry the project roots the executor applies when the request has none")
	}
	runtimePath := filepath.Join(contractcheck.RepoRoot(t), "lycaon", "internal", "toolhost", "runtime.go")
	runtimeRaw, err := os.ReadFile(runtimePath)
	contractcheck.FailErr(t, "read toolhost/runtime.go", err)
	runtimeSrc := string(runtimeRaw)
	// The gate is built through the deferred builder, not inline: its producers do
	// not all exist at this point in boot, and a gate constructed before them
	// reads an unwired producer as silence.
	if !strings.Contains(runtimeSrc, "settings.NewGateBuilder(") {
		t.Fatal("toolhost/runtime.go must construct the approval gate through NewGateBuilder")
	}
	authority := contractcheck.ReadRepoFile(t, contractcheck.RepoRoot(t), "lycaon/internal/toolhost/authority_services.go")
	if !strings.Contains(authority, "func (r *AuthorityServices) SealApprovalGate()") {
		t.Fatal("toolhost authority service must expose SealApprovalGate so boot can install the built gate")
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
