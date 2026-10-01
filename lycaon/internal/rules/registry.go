package rules

import (
	"github.com/lycaon/lycaon/internal/agentdef"
	"github.com/lycaon/lycaon/internal/conditions"
)

// RegisterRuleConditions registers rule-engine condition primitives.
func RegisterRuleConditions(reg *conditions.ConditionRegistry) error {
	if reg == nil {
		return nil
	}
	type entry struct {
		name string
		fn   func(EvalContext, StubValidator) (bool, error)
	}
	entries := []entry{
		{"stub_valid", func(_ EvalContext, stub StubValidator) (bool, error) {
			return stub.Valid(), nil
		}},
		{"stub_invalid", func(_ EvalContext, stub StubValidator) (bool, error) {
			return !stub.Valid(), nil
		}},
		{"tool_is_workflow_advance", func(e EvalContext, _ StubValidator) (bool, error) {
			return e.ToolName == "workflow_advance", nil
		}},
		{"tool_is_workflow_transition", func(e EvalContext, _ StubValidator) (bool, error) {
			return e.ToolName == "workflow_transition", nil
		}},
		{"research_task", func(e EvalContext, _ StubValidator) (bool, error) {
			return isResearchAgentTask(e.ToolArgs), nil
		}},
		{"critic_task", func(e EvalContext, _ StubValidator) (bool, error) {
			return isCriticAgentTask(e.ToolArgs), nil
		}},
		{"pack_runner_task", func(e EvalContext, _ StubValidator) (bool, error) {
			return isImplementerTask(e.ToolArgs), nil
		}},
	}
	for _, ent := range entries {
		name := ent.name
		evalFn := ent.fn
		if err := reg.Register(name, func(wctx conditions.EvalContext) (bool, error) {
			re := EvalContext{EvalContext: wctx}
			return evalFn(re, stubFromEval(re))
		}); err != nil {
			return err
		}
	}
	for _, pair := range []struct {
		name string
		leaf string
	}{
		{"plan_not_approved", "human_approval"},
		{"handoff_not_ready", "implement_workflow_ready"},
	} {
		leaf := pair.leaf
		if err := reg.Register(pair.name, func(wctx conditions.EvalContext) (bool, error) {
			ok, err := reg.Evaluate(leaf, wctx)
			return err == nil && !ok, err
		}); err != nil {
			return err
		}
	}
	return nil
}

// The dispatch class comes from the capabilities the agent declares, so a new
// agent is classified by its own YAML rather than by editing a list here.
func isResearchAgentTask(args map[string]any) bool {
	agent, _ := args["agent_type"].(string)
	return agentdef.DeclaresAny(agent, agentdef.DiscoveryCapabilities()...)
}

func isCriticAgentTask(args map[string]any) bool {
	agent, _ := args["agent_type"].(string)
	return agentdef.DeclaresAny(agent, agentdef.CritiqueCapabilities()...)
}
