package toolexecution

import (
	"context"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/pkg/api"
)

// evaluatePreSpawn includes detection in the capability review.
func (e *Approvals) evaluatePreSpawn(
	ctx context.Context,
	action hitl.ProposedAction,
) (*hitl.ApprovalResult, error) {
	if e == nil || e.approvalGate == nil {
		return nil, nil
	}
	return e.approvalGate.Evaluate(ctx, action)
}

func approvalRuleMatches(result *hitl.ApprovalResult) []hitl.ApprovalRuleMatch {
	if result == nil {
		return nil
	}
	return append([]hitl.ApprovalRuleMatch(nil), result.MatchedRules...)
}

// approvalDecision is nil for silent or enforced outcomes.
func approvalDecision(result *hitl.ApprovalResult) *gate.Decision {
	if result == nil {
		return nil
	}
	return result.Decision
}

func approvalGrantDelta(result *hitl.ApprovalResult) string {
	if result == nil {
		return ""
	}
	return result.GrantDelta
}

// detectionOf returns the ledger citation when a detection contributed.
func detectionOf(result *hitl.ApprovalResult) *hitl.DetectionMatch {
	if result == nil || result.Decision == nil {
		return nil
	}
	for _, g := range result.Decision.Gates() {
		if g == api.GateAuthorityMisuse {
			return result.DetectionCitation
		}
	}
	return nil
}
