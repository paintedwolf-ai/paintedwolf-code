package tools

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/pkg/api"
)

const coordinatorProfileID = "coordinator"

// GuidanceRejectPolicy wraps a policy engine and classifies unclassified blocks.
type GuidanceRejectPolicy struct {
	inner platform.PolicyEngine
}

// NewGuidanceRejectPolicy constructs a guidance-aware policy wrapper.
func NewGuidanceRejectPolicy(inner platform.PolicyEngine) *GuidanceRejectPolicy {
	return &GuidanceRejectPolicy{inner: inner}
}

// EvaluateForList delegates to inner when it implements ListVisiblePolicy so approval
// "ask" tools (e.g. delete) remain in the LLM schema; checkpoints run at invoke.
func (p *GuidanceRejectPolicy) EvaluateForList(ctx context.Context, eval platform.PolicyContext) (*platform.PolicyDecision, error) {
	if p == nil || p.inner == nil {
		return &platform.PolicyDecision{Allowed: true}, nil
	}
	if lister, ok := p.inner.(ListVisiblePolicy); ok {
		return lister.EvaluateForList(ctx, eval)
	}
	return p.inner.Evaluate(ctx, eval)
}

// Evaluate runs inner policy and classifies a block the inner engine left
// uncoded. The executor performs observation and rendering.
func (p *GuidanceRejectPolicy) Evaluate(ctx context.Context, eval platform.PolicyContext) (*platform.PolicyDecision, error) {
	decision, err := p.inner.Evaluate(ctx, eval)
	if err != nil {
		return nil, err
	}
	if decision == nil || !decision.Blocked || decision.RejectCode != "" {
		return decision, nil
	}
	tr := policyBlockObservation(eval)
	if tr == nil || tr.Code == "" {
		return decision, nil
	}
	decision.RejectCode = tr.Code
	decision.RejectData = tr.Data
	decision.BlockReason = tr.Code
	return decision, nil
}

// policyBlockReject is the refusal a blocked decision hands the executor.
func policyBlockReject(decision *platform.PolicyDecision, tool, profileID string) *ToolReject {
	code := ""
	var data map[string]any
	if decision != nil {
		code = strings.TrimSpace(decision.RejectCode)
		data = decision.RejectData
	}
	if code == "" {
		code = "TOOL_PROFILE_DENIED"
	}
	if data == nil {
		data = map[string]any{"tool": tool, "profile": profileID}
	}
	return &ToolReject{Code: code, Data: data, FailureClass: api.FailureClassPolicyRejection}
}

func policyBlockObservation(eval platform.PolicyContext) *ToolReject {
	data := map[string]any{
		"tool":    eval.ToolName,
		"profile": eval.ProfileID,
	}
	code := "TOOL_PROFILE_DENIED"
	if eval.ProfileID == coordinatorProfileID {
		code = "COORDINATOR_TOOL_DENIED"
	}
	return &ToolReject{Code: code, Data: data}
}
