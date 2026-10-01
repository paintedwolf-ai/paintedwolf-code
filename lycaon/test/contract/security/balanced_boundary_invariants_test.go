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

// WriteRootsForProject and BuildProfile share the same write-root union.
func TestInvariantWriteRootSSOTSharedByGateAndConfine(t *testing.T) {
	t.Parallel()
	proj := t.TempDir()
	writeRoots := confine.WriteRootsForProject("", []string{proj})
	if len(writeRoots) == 0 {
		t.Fatal("WriteRootsForProject returned no roots")
	}

	// /tmp survives EvalSymlinks (→ /private/tmp on darwin) and is in the union.
	if !confine.PathWithinWriteRoots("/tmp/scratch", writeRoots) {
		t.Fatalf("/tmp/scratch must be within write roots: %v", writeRoots)
	}
	// A path outside every write root is not.
	if confine.PathWithinWriteRoots("/etc/lycaon-probe", writeRoots) {
		t.Fatalf("/etc must not be within write roots: %v", writeRoots)
	}

}

// Contained is stamped from DefaultConfinement onto ProposedAction.
func TestInvariantProductionGateWiresContainment(t *testing.T) {
	t.Parallel()
	policyPath := filepath.Join(contractcheck.RepoRoot(t), "lycaon", "internal", "tools", "approval_policy.go")
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
	if !strings.Contains(runtimeSrc, "func (r *Runtime) SealApprovalGate()") {
		t.Fatal("toolhost/runtime.go must expose SealApprovalGate so boot can install the built gate")
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
