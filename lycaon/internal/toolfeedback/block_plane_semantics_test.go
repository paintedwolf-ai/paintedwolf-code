package toolfeedback

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/curationctx"
	"github.com/lycaon/lycaon/internal/oar"
)

func TestBlockPlaneCarriesSessionIdentityIntoOccurrence(t *testing.T) {
	rule := &oar.Rule{
		ID: "IDENTITY", Kind: oar.KindPolicy, Anchor: oar.AnchorToolPreInvoke,
		Effect: oar.EffectBlock, Enforcement: "enforce", When: `principal == "owner-person"`,
	}
	pipeline := oar.NewGuardPipeline(oar.NewRuleSet([]*oar.Rule{rule}), nil, nil)
	pipeline.EnableAnchor(oar.AnchorToolPreInvoke)
	bp := &BlockPlane{Pipeline: pipeline}
	ctx := curationctx.WithSession(t.Context(), curationctx.Session{SessionID: "session-1", Agent: "implement", OwnerPersonID: "owner-person"})
	if err := bp.Evaluate(ctx, oar.AnchorToolPreInvoke, "write", "implement", nil, nil); err == nil {
		t.Fatal("identity rule did not observe the invoking principal")
	}
}

func TestBlockPlaneWarnDoesNotRejectTool(t *testing.T) {
	rule := &oar.Rule{
		ID: "ADVISORY", Kind: oar.KindPolicy, Anchor: oar.AnchorToolPreInvoke,
		Effect: oar.EffectWarn, Enforcement: "enforce",
	}
	pipeline := oar.NewGuardPipeline(oar.NewRuleSet([]*oar.Rule{rule}), nil, nil)
	pipeline.EnableAnchor(oar.AnchorToolPreInvoke)
	bp := &BlockPlane{Pipeline: pipeline}
	if err := bp.Evaluate(t.Context(), oar.AnchorToolPreInvoke, "read", "implement", nil, nil); err != nil {
		t.Fatalf("warn rejected tool: %v", err)
	}
}

func TestBlockPlanePropagatesObservationFailureWithoutInventingPolicyDecision(t *testing.T) {
	rule := &oar.Rule{ID: "BLOCK", Kind: oar.KindPolicy, Anchor: oar.AnchorToolPreInvoke, Effect: oar.EffectBlock, Enforcement: "enforce"}
	pipeline := oar.NewGuardPipeline(oar.NewRuleSet([]*oar.Rule{rule}), nil, nil)
	pipeline.EnableAnchor(oar.AnchorToolPreInvoke)
	failure := errors.New("catalog observation unavailable")
	bp := &BlockPlane{Pipeline: pipeline}
	got := bp.Evaluate(t.Context(), oar.AnchorToolPreInvoke, "read", "implement", nil, func(*oar.GuardContext) error { return failure })
	if !errors.Is(got, failure) {
		t.Fatalf("observation failure changed into decision: %v", got)
	}
	pipeline = oar.NewGuardPipeline(oar.NewRuleSet([]*oar.Rule{rule}), nil, nil)
	bp.Pipeline = pipeline
	called := false
	if err := bp.Evaluate(t.Context(), oar.AnchorToolPreInvoke, "read", "implement", nil, func(*oar.GuardContext) error { called = true; return failure }); err != nil || called {
		t.Fatalf("disabled anchor observed or rejected: called=%t err=%v", called, err)
	}
}

func TestBlockPlaneWithoutAuthorityDoesNotObserveOrReject(t *testing.T) {
	for _, plane := range []*BlockPlane{nil, {}} {
		if plane.Enforces(oar.AnchorToolPreInvoke) {
			t.Fatal("absent pipeline claims enforcement")
		}
		if err := plane.Evaluate(t.Context(), oar.AnchorToolPreInvoke, "read", "implement", nil, func(*oar.GuardContext) error { t.Fatal("absent pipeline observed"); return nil }); err != nil {
			t.Fatal(err)
		}
	}
}
