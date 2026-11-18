package oartest

import (
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/testutil"
)

func NewCoordinatorGuardPipeline(t *testing.T) *oar.GuardPipeline {
	t.Helper()
	policy := effectiveCloseoutPolicy(t)
	pipeline := oar.NewGuardPipeline(policy.rules, policy.loader, oar.NewCounterStore())
	pipeline.EnableAnchor(oar.AnchorCoordinatorCloseoutCheck)
	pipeline.EnableAnchor(oar.AnchorCoordinatorMidTurnNote)
	pipeline.EnableAnchor(oar.AnchorCoordinatorPreInvoke)
	pipeline.EnableAnchor(oar.AnchorToolPreInvoke)
	return pipeline
}

func EvaluateHasCode(t *testing.T, facts *oar.GuardContext, anchor, code string) bool {
	t.Helper()
	decision, err := NewCoordinatorGuardPipeline(t).EvaluateBlock(t.Context(), anchor, facts)
	testutil.FailErr(t, "evaluate guard", err)
	return decision != nil && decision.Decision != nil && decision.Decision.Code == code
}

func FormatFirstBlock(t *testing.T, facts *oar.GuardContext, formatter *guidance.StaticRejectFormatter, anchors ...string) (string, bool) {
	t.Helper()
	pipeline := NewCoordinatorGuardPipeline(t)
	for _, anchor := range anchors {
		block, err := pipeline.EvaluateBlock(t.Context(), anchor, facts)
		testutil.FailErr(t, "evaluate guard", err)
		if block == nil || block.Decision == nil {
			continue
		}
		decision := block.Decision
		formatted, err := formatter.Format(decision.Code, decision.Data)
		testutil.FailErr(t, "format guard rejection", err)
		return formatted, true
	}
	return "", false
}
