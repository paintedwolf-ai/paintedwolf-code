package rules

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// hintRulePairs are rule ids whose when: map must match a hint registry when string.
var hintRulePairs = []struct {
	ruleID   string
	hintWhen string
}{
	{"spec_posture_unresolved", "posture_unresolved and high_risk_tool"},
	{"spec_posture_no_delegation_dispatch", "not plan_awaiting_approval and posture_is_spec and tool_is_delegation"},
	{"spec_posture_delegation_awaiting_approval", "plan_awaiting_approval and posture_is_spec and tool_is_delegation"},
	{"spec_posture_disallowed_agent", "posture_is_spec and tool_is_task and disallowed_agent"},
	{"spec_posture_stub_required_plan_writer", "posture_is_spec and tool_is_task and stub_invalid and agent_is_plan_writer"},
}

func loadSpecRules(t *testing.T) *RulesConfig {
	t.Helper()
	cfg, err := LoadRulesConfig(config.PostureRulesDir.Join("spec.yaml"))
	testutil.FailErr(t, "LoadRulesConfig failed", err)
	return cfg
}

func ruleTestRegistry(t *testing.T) *conditions.ConditionRegistry {
	t.Helper()
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	testutil.FailErr(t, "build conditions registry", err)
	testutil.FailErr(t, "RegisterRuleConditions", RegisterRuleConditions(reg))
	return reg
}

// validStubPlan contains the required stub sections.
const validStubPlan = "---\ntitle: Ship it\nresearch_depth: none\n---\n## Goal\n\nx\n\n## Assumptions\n\nx\n\n## Plan implementation scope\n\n**Size:** small\n\n## Plan breaking changes\n\nnone\n"

// TestMapWhenMatchesHintDSLAcrossContexts checks map and string equivalence.
func TestMapWhenMatchesHintDSLAcrossContexts(t *testing.T) {
	cfg := loadSpecRules(t)
	byID := map[string]RuleEntry{}
	for _, r := range cfg.Rules {
		byID[r.ID] = r
	}
	reg := ruleTestRegistry(t)
	contexts := []struct {
		name string
		eval EvalContext
	}{
		{"spec state stub invalid", EvalContext{EvalContext: conditions.EvalContext{
			SessionPosture: api.SessionPostureSpec,
			ToolName:       "state_create",
		}}},
		{"spec delegate", EvalContext{EvalContext: conditions.EvalContext{
			SessionPosture: api.SessionPostureSpec,
			ToolName:       "delegate_dispatch",
			PlanContent:    validStubPlan,
		}}},
		{"unresolved high risk", EvalContext{EvalContext: conditions.EvalContext{
			SessionPosture: "",
			ToolName:       "delegate_dispatch",
		}}},
		{"build delegate ok", EvalContext{EvalContext: conditions.EvalContext{
			SessionPosture: api.SessionPostureBuild,
			ToolName:       "delegate_dispatch",
			PlanContent:    validStubPlan,
		}}},
		{"spec task disallowed agent", EvalContext{EvalContext: conditions.EvalContext{
			SessionPosture: api.SessionPostureSpec,
			ToolName:       "task",
			ToolArgs:       map[string]any{"agent_type": "implementer"},
			AllowedAgents:  []string{"plan-writer"},
			PlanContent:    validStubPlan,
		}}},
	}
	for _, pair := range hintRulePairs {
		rule, ok := byID[pair.ruleID]
		if !ok {
			t.Fatalf("missing rule %q", pair.ruleID)
		}
		canon, err := CanonicalWhenString(rule.When)
		if err != nil {
			t.Fatalf("%s: %v", pair.ruleID, err)
		}
		for _, ctx := range contexts {
			mapMatch, err := MatchWhen(reg, rule.When, ctx.eval)
			testutil.FailErr(t, "MatchWhen", err)
			hintMatch, err := MatchWhenExpr(reg, pair.hintWhen, ctx.eval)
			testutil.FailErr(t, "MatchWhenExpr hint", err)
			canonMatch, err := MatchWhenExpr(reg, canon, ctx.eval)
			testutil.FailErr(t, "MatchWhenExpr canonical", err)
			if mapMatch != hintMatch || mapMatch != canonMatch {
				t.Errorf("%s %s: map=%v hint=%v canon=%v (%q / %q)",
					pair.ruleID, ctx.name, mapMatch, hintMatch, canonMatch, pair.hintWhen, canon)
			}
		}
	}
}

// TestMatchWhenRequiresRegistry enforces one evaluation path.
func TestMatchWhenRequiresRegistry(t *testing.T) {
	if _, err := MatchWhen(nil, map[string]any{"posture_unresolved": true}, EvalContext{}); err == nil {
		t.Fatal("MatchWhen with nil registry must error, not fall back to a second evaluator")
	}
}

func TestEngineOrderingFirstMatchWins(t *testing.T) {
	engine := NewSimpleEngine(loadSpecRules(t), ruleTestRegistry(t))
	out, err := engine.Evaluate(context.Background(), EvalContext{EvalContext: conditions.EvalContext{
		SessionPosture: api.SessionPostureSpec,
		ToolName:       "delegate_dispatch",
	}})
	testutil.FailErr(t, "engine.Evaluate failed", err)
	if out.Allowed || out.Code != "SPEC_POSTURE_DELEGATION_FORBIDDEN" {
		t.Fatalf("evaluate = %+v", out)
	}
}
