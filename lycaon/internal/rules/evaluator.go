package rules

import (
	"strings"

	"github.com/lycaon/lycaon/internal/conditions"
)

func evaluateOrderedRules(registry *conditions.ConditionRegistry, rules []RuleEntry, eval EvalContext) (*RuleOutcome, error) {
	for _, rule := range rules {
		ok, err := MatchWhen(registry, rule.When, eval)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		return outcomeFromThen(rule), nil
	}
	return &RuleOutcome{Allowed: true}, nil
}

func truthy(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return t != "" && t != "false"
	default:
		return v != nil
	}
}

func isImplementerTask(args map[string]any) bool {
	agent, _ := args["agent_type"].(string)
	return strings.EqualFold(agent, "implementer")
}

func outcomeFromThen(rule RuleEntry) *RuleOutcome {
	then := rule.Then
	if then == nil {
		return &RuleOutcome{Allowed: true}
	}
	if deny, ok := then["deny"].(map[string]any); ok {
		code, _ := deny["code"].(string)
		msg, _ := deny["message"].(string)
		out := &RuleOutcome{
			Allowed:    false,
			Code:       code,
			Message:    msg,
			RejectCode: code,
		}
		if v, _ := deny["phase_required"].(string); v != "" {
			out.PhaseRequired = v
		}
		if v, _ := deny["phase_required_name"].(string); v != "" {
			out.PhaseRequiredName = v
		}
		if v, _ := deny["min_required"].(string); v != "" {
			out.MinRequired = v
		}
		if v, _ := deny["max_playbook"].(string); v != "" {
			out.MaxPlaybook = v
		}
		return out
	}
	return &RuleOutcome{Allowed: true}
}
