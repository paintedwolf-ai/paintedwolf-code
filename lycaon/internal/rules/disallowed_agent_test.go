package rules

import (
	"context"
	"github.com/lycaon/lycaon/internal/conditions"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestDisallowedAgentRule(t *testing.T) {
	reg := ruleTestRegistry(t)
	engine := NewSimpleEngine(&RulesConfig{Rules: []RuleEntry{{
		ID: "deny_bad_agent",
		When: map[string]any{
			"tool_is_task":     true,
			"disallowed_agent": true,
		},
		Then: map[string]any{
			"deny": map[string]any{
				"code":    "DISALLOWED_AGENT",
				"message": "not allowed",
			},
		},
	}}}, reg)
	outcome, err := engine.Evaluate(context.Background(), EvalContext{EvalContext: conditions.EvalContext{ToolName: "task", ToolArgs: map[string]any{"agent_type": "sentinel"}, AllowedAgents: []string{
		"plan-writer", "repo-researcher",
	}}})
	testutil.FailErr(t, "engine.Evaluate failed", err)
	if outcome.Allowed {
		t.Fatal("expected deny for disallowed agent")
	}
	if outcome.Code != "DISALLOWED_AGENT" {
		t.Fatalf("code = %q", outcome.Code)
	}

	outcome, err = engine.Evaluate(context.Background(), EvalContext{EvalContext: conditions.EvalContext{ToolName: "task", ToolArgs: map[string]any{"agent_type": "plan-writer"}, AllowedAgents: []string{
		"plan-writer", "repo-researcher",
	}}})
	testutil.FailErr(t, "engine.Evaluate failed", err)
	if !outcome.Allowed {
		t.Fatalf("expected allow for plan-writer, got %q", outcome.Code)
	}
}

func TestDisallowedAgentIgnoredWithoutAllowlist(t *testing.T) {
	reg := ruleTestRegistry(t)
	engine := NewSimpleEngine(&RulesConfig{Rules: []RuleEntry{{
		When: map[string]any{
			"tool_is_task":     true,
			"disallowed_agent": true,
		},
		Then: map[string]any{
			"deny": map[string]any{"code": "DISALLOWED_AGENT"},
		},
	}}}, reg)
	outcome, err := engine.Evaluate(context.Background(), EvalContext{EvalContext: conditions.EvalContext{SessionPosture: api.SessionPostureOrchestrate, ToolName: "task", ToolArgs: map[string]any{"agent_type": "implementer"}}})
	testutil.FailErr(t, "engine.Evaluate failed", err)
	if !outcome.Allowed {
		t.Fatal("expected allow when workflow allowlist is empty")
	}
}

// A roster that filters out every declared agent leaves an empty, non-nil allowlist:
// any dispatch gets DISALLOWED_AGENT, never the unfiltered declared list.
func TestDisallowedAgentEmptyEffectiveRosterDeniesAll(t *testing.T) {
	reg := ruleTestRegistry(t)
	engine := NewSimpleEngine(&RulesConfig{Rules: []RuleEntry{{
		ID: "deny_bad_agent",
		When: map[string]any{
			"tool_is_task":     true,
			"disallowed_agent": true,
		},
		Then: map[string]any{
			"deny": map[string]any{
				"code":    "DISALLOWED_AGENT",
				"message": "not allowed",
			},
		},
	}}}, reg)
	outcome, err := engine.Evaluate(context.Background(), EvalContext{EvalContext: conditions.EvalContext{
		ToolName: "task", ToolArgs: map[string]any{"agent_type": "implementer"},
		AllowedAgents: []string{},
	}})
	testutil.FailErr(t, "engine.Evaluate failed", err)
	if outcome.Allowed {
		t.Fatal("expected deny when the effective roster is attached and empty")
	}
	if outcome.Code != "DISALLOWED_AGENT" {
		t.Fatalf("code = %q", outcome.Code)
	}
}
