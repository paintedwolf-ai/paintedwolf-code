package contract

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/oar"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// TestOARConformanceSeededFromWhenRules runs seeded rule fixtures.
func TestOARConformanceSeededFromWhenRules(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	out := t.TempDir()
	codes := []string{
		"SPEC_POSTURE_STATE_FORBIDDEN",
		"SPEC_POSTURE_DELEGATION_FORBIDDEN",
		"SPEC_POSTURE_HANDOFF_FORBIDDEN",
		"SPEC_POSTURE_IMPLEMENT_FORBIDDEN",
		"SPEC_POSTURE_UNRESOLVED",
		"SPEC_POSTURE_STUB_REQUIRED",
		"DISALLOWED_AGENT",
	}
	// An empty policy dir means the shipped policy set — the seeds under test.
	if err := oar.SeedFixturesFromScenarios("", out, codes); err != nil {
		contractcheck.FailErr(t, "seed fixtures", err)
	}
	cr, err := oar.NewConformanceRunner(filepath.Join(root, "schemas"))
	contractcheck.FailErr(t, "conformance runner", err)
	fxs, err := oar.LoadFixturesDir(out)
	contractcheck.FailErr(t, "load fixtures", err)
	if len(fxs) != len(codes) {
		t.Fatalf("expected %d fixtures, got %d", len(codes), len(fxs))
	}
	for _, fx := range fxs {
		if err := cr.RunFixture(fx); err != nil {
			t.Errorf("%s: %v", fx.Name, err)
		}
	}
}

// TestOARPipelineEvaluateBlockDormantWithoutEnableAnchor documents production dormancy:
// EvaluateBlock is a no-op until EnableAnchor for that catalog Anchor.
func TestOARPipelineEvaluateBlockDormantWithoutEnableAnchor(t *testing.T) {
	t.Parallel()
	p := oar.NewGuardPipeline(oar.NewRuleSet(nil), nil, oar.NewCounterStore())
	res, err := p.EvaluateBlock(context.Background(), oar.AnchorToolPreInvoke, oar.NewGuardContext())
	contractcheck.FailErr(t, "EvaluateBlock", err)
	if res.Enforced || res.Decision != nil {
		t.Fatalf("EvaluateBlock must stay dormant without EnableAnchor, got %#v", res)
	}
}
