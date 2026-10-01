package tools

import (
	"context"

	"github.com/lycaon/lycaon/internal/platform"
)

// ListVisiblePolicy evaluates whether a tool belongs in the LLM schema for a profile.
// Approval rules with effect "ask" still appear on the wire; checkpoints run at invoke.
type ListVisiblePolicy interface {
	platform.PolicyEngine
	EvaluateForList(ctx context.Context, eval platform.PolicyContext) (*platform.PolicyDecision, error)
}

// EvaluateListVisible returns profile allowlist visibility for tool listing.
func EvaluateListVisible(ctx context.Context, policy platform.PolicyEngine, eval platform.PolicyContext) (*platform.PolicyDecision, error) {
	if policy == nil {
		return &platform.PolicyDecision{Allowed: true}, nil
	}
	if lister, ok := policy.(ListVisiblePolicy); ok {
		return lister.EvaluateForList(ctx, eval)
	}
	return policy.Evaluate(ctx, eval)
}
