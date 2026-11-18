package rules

import (
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestDisallowedAgentRule(t *testing.T) {
	reg := ruleTestRegistry(t)
	rules := []RuleEntry{{
		ID: "deny_bad_agent",
		When: map[string]any{
			"tool_names_agent": true,
			"disallowed_agent": true,
		},
		Then: map[string]any{
			"deny": map[string]any{
				"code":    "DISALLOWED_AGENT",
				"message": "not allowed",
			},
		},
	}}
	outcome, err := evaluateOrderedRules(reg, rules, EvalContext{EvalContext: conditions.EvalContext{ToolName: "task", ToolArgs: map[string]any{"agent_type": "sentinel"}, AllowedAgents: []string{
		"plan-writer", "repo-researcher",
	}}})
	testutil.FailErr(t, "engine.Evaluate failed", err)
	if outcome.Allowed {
		t.Fatal("expected deny for disallowed agent")
	}
	if outcome.Code != "DISALLOWED_AGENT" {
		t.Fatalf("code = %q", outcome.Code)
	}

	outcome, err = evaluateOrderedRules(reg, rules, EvalContext{EvalContext: conditions.EvalContext{ToolName: "task", ToolArgs: map[string]any{"agent_type": "plan-writer"}, AllowedAgents: []string{
		"plan-writer", "repo-researcher",
	}}})
	testutil.FailErr(t, "engine.Evaluate failed", err)
	if !outcome.Allowed {
		t.Fatalf("expected allow for plan-writer, got %q", outcome.Code)
	}
}

// Changing a worker's agent with instruct_worker meets the same roster;
// instructing it without naming an agent changes nothing to check.
func TestDisallowedAgentCoversAgentChanges(t *testing.T) {
	reg := ruleTestRegistry(t)
	rules := []RuleEntry{{
		ID:   "deny_bad_agent",
		When: map[string]any{"tool_names_agent": true, "disallowed_agent": true},
		Then: map[string]any{"deny": map[string]any{"code": "DISALLOWED_AGENT"}},
	}}
	roster := []string{"repo-researcher"}
	outcome, err := evaluateOrderedRules(reg, rules, EvalContext{EvalContext: conditions.EvalContext{ToolName: "instruct_worker", ToolArgs: map[string]any{"worker": "scout", "agent_type": "implementer"}, AllowedAgents: roster}})
	testutil.FailErr(t, "evaluate agent change", err)
	if outcome.Allowed || outcome.Code != "DISALLOWED_AGENT" {
		t.Fatalf("agent change outside the roster = %+v", outcome)
	}
	outcome, err = evaluateOrderedRules(reg, rules, EvalContext{EvalContext: conditions.EvalContext{ToolName: "instruct_worker", ToolArgs: map[string]any{"worker": "scout"}, AllowedAgents: roster}})
	testutil.FailErr(t, "evaluate instruction", err)
	if !outcome.Allowed {
		t.Fatalf("instruction without an agent = %+v", outcome)
	}
}

func TestDisallowedAgentIgnoredWithoutAllowlist(t *testing.T) {
	reg := ruleTestRegistry(t)
	rules := []RuleEntry{{
		When: map[string]any{
			"tool_names_agent": true,
			"disallowed_agent": true,
		},
		Then: map[string]any{
			"deny": map[string]any{"code": "DISALLOWED_AGENT"},
		},
	}}
	outcome, err := evaluateOrderedRules(reg, rules, EvalContext{EvalContext: conditions.EvalContext{SessionPosture: api.SessionPostureOrchestrate, ToolName: "task", ToolArgs: map[string]any{"agent_type": "implementer"}}})
	testutil.FailErr(t, "engine.Evaluate failed", err)
	if !outcome.Allowed {
		t.Fatal("expected allow when workflow allowlist is empty")
	}
}

// A roster that filters out every declared agent leaves an empty, non-nil allowlist:
// any dispatch gets DISALLOWED_AGENT, never the unfiltered declared list.
func TestDisallowedAgentEmptyEffectiveRosterDeniesAll(t *testing.T) {
	reg := ruleTestRegistry(t)
	rules := []RuleEntry{{
		ID: "deny_bad_agent",
		When: map[string]any{
			"tool_names_agent": true,
			"disallowed_agent": true,
		},
		Then: map[string]any{
			"deny": map[string]any{
				"code":    "DISALLOWED_AGENT",
				"message": "not allowed",
			},
		},
	}}
	outcome, err := evaluateOrderedRules(reg, rules, EvalContext{EvalContext: conditions.EvalContext{
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
