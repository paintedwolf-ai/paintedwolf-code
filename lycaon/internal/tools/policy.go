package tools

import (
	"context"

	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/sandbox"
)

// DefaultToolProfileID applies when no profile or agent is selected.
const DefaultToolProfileID = "implement"

// ProfilePolicyEngine evaluates tool access via sandbox tool profiles.
type ProfilePolicyEngine struct {
	boundary        sandbox.SandboxBoundary
	runtimeToolDeny func(toolName string) bool
}

// NewProfilePolicyEngine constructs a policy engine backed by sandbox profiles.
func NewProfilePolicyEngine(boundary sandbox.SandboxBoundary) *ProfilePolicyEngine {
	return &ProfilePolicyEngine{boundary: boundary}
}

// SetRuntimeToolDeny sets host-level tool exclusions.
func (p *ProfilePolicyEngine) SetRuntimeToolDeny(deny func(toolName string) bool) {
	if p == nil {
		return
	}
	p.runtimeToolDeny = deny
}

func (p *ProfilePolicyEngine) WaitConditions(profileID string) []string {
	if p == nil {
		return nil
	}
	if source, ok := p.boundary.(interface{ WaitConditions(string) []string }); ok {
		return source.WaitConditions(profileID)
	}
	return nil
}

// Evaluate checks whether a tool is allowed for the profile.
func (p *ProfilePolicyEngine) Evaluate(ctx context.Context, eval platform.PolicyContext) (*platform.PolicyDecision, error) {
	if denied, code := p.runtimeDenied(eval.ToolName); denied {
		return &platform.PolicyDecision{
			Blocked:     true,
			RejectCode:  code,
			RejectData:  map[string]any{"tool": eval.ToolName, "profile": eval.ProfileID},
			BlockReason: code,
		}, nil
	}
	profileID := eval.ProfileID
	if profileID == "" {
		profileID = DefaultToolProfileID
	}
	if err := p.boundary.AssertToolAllowed(ctx, profileID, eval.ToolName, eval.ToolAccess); err != nil {
		return &platform.PolicyDecision{ //nolint:nilerr // err.Error() carried as BlockReason
			Blocked:     true,
			BlockReason: err.Error(),
		}, nil
	}
	return &platform.PolicyDecision{
		Allowed:  true,
		Deferred: p.boundary.ToolDeferred(profileID, eval.ToolName, eval.ToolAccess),
	}, nil
}

// EvaluateForList hides runtime-denied tools from the LLM schema; approval "ask"
// tools still appear when the profile allows them.
func (p *ProfilePolicyEngine) EvaluateForList(ctx context.Context, eval platform.PolicyContext) (*platform.PolicyDecision, error) {
	if denied, _ := p.runtimeDenied(eval.ToolName); denied {
		return &platform.PolicyDecision{Allowed: false}, nil
	}
	profileID := eval.ProfileID
	if profileID == "" {
		profileID = DefaultToolProfileID
	}
	if err := p.boundary.AssertToolAllowed(ctx, profileID, eval.ToolName, eval.ToolAccess); err != nil {
		return &platform.PolicyDecision{Allowed: false}, nil //nolint:nilerr // an authz denial is the decision, not a policy-evaluation failure
	}
	return &platform.PolicyDecision{
		Allowed:  true,
		Deferred: p.boundary.ToolDeferred(profileID, eval.ToolName, eval.ToolAccess),
	}, nil
}

// runtimeDenied reports a host-level exclusion and its reject code.
func (p *ProfilePolicyEngine) runtimeDenied(toolName string) (bool, string) {
	if p != nil && p.runtimeToolDeny != nil && p.runtimeToolDeny(toolName) {
		return true, "WEB_SEARCH_DISABLED"
	}
	return false, ""
}
