package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// mintSeamFiles are the only files that construct a tool_approval checkpoint
// request, so one place interrupts a person and one place decides why. The
// sandbox capability brokers share awaitSandboxAsk, so only it appears here;
// another broker entry would be a second copy of that lifecycle.
var mintSeamFiles = map[string]string{
	"lycaon/internal/tools/executor_tool_approval.go":       "the executor's raise-and-wait seam",
	"lycaon/internal/session/sandbox_ask_broker.go":         "the sandbox capability ask lifecycle",
	"lycaon/internal/tools/socket_approval.go":              "the local-socket capability card",
	"lycaon/internal/toolexecution/direct_ip_capability.go": "the direct-network capability card",
}

// TestApprovalsHaveOneMintSeam keeps approval creation on reviewed seams.
func TestApprovalsHaveOneMintSeam(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	var offenders []string
	walkGoSources(t, filepath.Join(root, "lycaon"), func(rel, src string) {
		if _, allowed := mintSeamFiles[rel]; allowed || strings.HasSuffix(rel, "_test.go") {
			return
		}
		if strings.Contains(src, "hitl.CheckpointRequest{") &&
			strings.Contains(src, "api.CheckpointKindToolApproval") {
			offenders = append(offenders, rel)
		}
	})
	if len(offenders) > 0 {
		t.Fatalf("tool_approval minted outside the seam: %v\nroute these through the executor's raise path so one gate decides every interruption", offenders)
	}
}

// TestEveryApprovalNamesAGate fails when a checkpoint can be raised without a
// decision; a card that cannot name its gate cannot be explained.
func TestEveryApprovalNamesAGate(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	src := contractcheck.ReadRepoFile(t, root, "lycaon/internal/hitl/checkpoint.go")
	if !strings.Contains(src, "Decision *gate.Decision") {
		t.Fatal("CheckpointRequest must carry the gate decision that made it an interruption")
	}
}

// TestGateLadderPolicyHasOneHome fails when a site keeps its own copy of which
// gates may offer reusable authority; gate.ReuseFor answers that question. The
// token is api.Gate, so the substring match passes vacuously if that generated
// prefix changes.
func TestGateLadderPolicyHasOneHome(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	// The two policy homes: reuse.go answers the ladder question, posture.go the
	// membership question. Both legitimately name many gates in one expression.
	policyHomes := map[string]bool{
		"lycaon/internal/gate/gate.go":    true,
		"lycaon/internal/gate/reuse.go":   true,
		"lycaon/internal/gate/posture.go": true,
	}
	var offenders []string
	walkGoSources(t, filepath.Join(root, "lycaon"), func(rel, src string) {
		if strings.HasSuffix(rel, "_test.go") || policyHomes[rel] ||
			strings.HasSuffix(rel, "_ids.generated.go") {
			return
		}
		// A site listing three or more gate constants in one expression is
		// re-deriving gate.ReuseFor instead of asking for it.
		for _, line := range strings.Split(src, "\n") {
			if strings.Count(line, "api.Gate") >= 3 || strings.Count(line, "api.GateSecretOutbound") > 0 &&
				strings.Count(line, "api.GateConsentDrift") > 0 {
				offenders = append(offenders, rel+": "+strings.TrimSpace(line))
			}
		}
	})
	if len(offenders) > 0 {
		t.Fatalf("reuse policy restated outside gate.ReuseFor:\n%s", strings.Join(offenders, "\n"))
	}
}

// TestEveryGateIsPostureReachableOrStructural mirrors the package-internal
// exhaustiveness test at the contract layer: every gate is enabled by a posture
// or is structural.
func TestEveryGateIsPostureReachableOrStructural(t *testing.T) {
	for _, g := range gate.All() {
		if g == api.GateIncompleteFacts {
			continue
		}
		reachable := false
		for _, p := range []gate.Posture{gate.PostureLight, gate.PostureBalanced, gate.PostureStrict} {
			if p.Enables(g) {
				reachable = true
			}
		}
		if !reachable {
			t.Errorf("%s is enabled by no posture", g)
		}
	}
}

func walkGoSources(t *testing.T, root string, visit func(rel, src string)) {
	t.Helper()
	repo := contractcheck.RepoRoot(t)
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		rel, relErr := filepath.Rel(repo, path)
		if relErr != nil {
			return nil
		}
		visit(filepath.ToSlash(rel), string(raw))
		return nil
	})
	contractcheck.FailErr(t, "walk go files", err)
}
