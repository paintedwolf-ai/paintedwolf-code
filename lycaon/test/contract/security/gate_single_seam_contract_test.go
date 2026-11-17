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
