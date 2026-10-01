package contract

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/oar"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// [OAR-EVAL-1] Intrinsic refusals are selected by their typed identity,
// not by the overlapping observation tokens from an earlier phase.
func TestSandboxCapabilityRulesBindActualRefusals(t *testing.T) {
	pipeline := stockRejectionPipeline(t)
	checked := 0
	loader, err := oar.NewLoader(filepath.Join(contractcheck.RepoRoot(t), "schemas"))
	contractcheck.FailErr(t, "create policy loader", err)
	rules, err := loader.LoadEffectivePolicy()
	contractcheck.FailErr(t, "load stock policy", err)
	for _, rule := range rules.All() {
		if !strings.HasPrefix(rule.ID, "SANDBOX_") || rule.Anchor != oar.AnchorToolRejected {
			continue
		}
		for _, tool := range rule.Selector["tool"] {
			assertIntrinsicRejection(t, pipeline, tool, rule.ID)
			checked++
		}
	}
	if checked == 0 {
		t.Fatal("no sandbox refusals exercised")
	}
}

func stockRejectionPipeline(t *testing.T) *oar.GuardPipeline {
	t.Helper()
	contractcheck.FailErr(t, "install anchors", anchorcatalog.InstallBundled())
	loader, err := oar.NewLoader(filepath.Join(contractcheck.RepoRoot(t), "schemas"))
	contractcheck.FailErr(t, "create policy loader", err)
	rules, err := loader.LoadEffectivePolicy()
	contractcheck.FailErr(t, "load stock policy", err)
	pipeline := oar.NewGuardPipeline(rules, loader, oar.NewCounterStore())
	pipeline.EnableAnchor(oar.AnchorToolRejected)
	return pipeline
}

func assertIntrinsicRejection(t *testing.T, pipeline *oar.GuardPipeline, tool, code string) {
	t.Helper()
	for _, observed := range []string{code, "UNRELATED_REFUSAL"} {
		gc := oar.NewGuardContext()
		gc.SessionID = t.Name() + tool + observed
		gc.Tool = tool
		gc.ObservedRejectCode = observed
		result, err := pipeline.EvaluateBlock(t.Context(), oar.AnchorToolRejected, gc)
		contractcheck.FailErr(t, "evaluate intrinsic refusal", err)
		if observed == code {
			if result == nil || result.Decision == nil || result.Decision.Code != code || len(result.Decision.Copy) != 5 {
				t.Fatalf("%s on %s lost policy: %+v", code, tool, result)
			}
		} else if result != nil && result.Decision != nil && result.Decision.Code == code {
			t.Fatalf("unrelated refusal on %s selected %s", tool, code)
		}
	}
}
