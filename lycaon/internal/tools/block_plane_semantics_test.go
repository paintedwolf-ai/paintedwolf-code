package tools

import (
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
