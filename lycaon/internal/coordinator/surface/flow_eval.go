package surface

import (
	"fmt"
	"strconv"
	"strings"
)

// EvaluateFlow walks table rules FIRST-hit against facts and resolves the output surface.
func EvaluateFlow(table FlowTable, facts SurfaceFacts) (FlowEvaluation, error) {
	vars := facts.AsMap()
	for i, rule := range table.Rules {
		if !ruleWhenHolds(rule.When, vars) {
			continue
		}
		surfaceID, err := resolveFlowOutput(rule.Out, vars)
		if err != nil {
			return FlowEvaluation{}, fmt.Errorf("coordinator flow: rule %d: %w", i, err)
		}
		base, err := CoordinatorSurfaceModeRef(surfaceID)
		if err != nil {
			return FlowEvaluation{}, fmt.Errorf("coordinator flow: rule %d: %w", i, err)
		}
		return FlowEvaluation{
			SurfaceID:      surfaceID,
			BaseModeRef:    base,
			MatchedRuleIdx: i,
		}, nil
	}
	surfaceID, err := resolveFlowOutput(table.Default, vars)
	if err != nil {
		return FlowEvaluation{}, fmt.Errorf("coordinator flow: default: %w", err)
	}
	base, err := CoordinatorSurfaceModeRef(surfaceID)
	if err != nil {
		return FlowEvaluation{}, fmt.Errorf("coordinator flow: default: %w", err)
	}
	return FlowEvaluation{
		SurfaceID:      surfaceID,
		BaseModeRef:    base,
		MatchedRuleIdx: -1,
	}, nil
}

func ruleWhenHolds(conds []FlowCondition, vars map[string]any) bool {
	for _, cond := range conds {
		if !flowConditionHolds(cond, vars) {
			return false
		}
	}
	return true
}

func flowConditionHolds(cond FlowCondition, vars map[string]any) bool {
	switch cond.Comparator {
	case FlowCmpGt0:
		n, ok := flowFactInt(vars, cond.Fact)
		return ok && n > 0
	case FlowCmpPresent:
		return flowFactPresent(vars, cond.Fact)
	case FlowCmpAbsent:
		return !flowFactPresent(vars, cond.Fact)
	default:
		return flowFactEq(vars, cond.Fact, cond.EqBool, cond.EqString)
	}
}

func flowFactEq(vars map[string]any, fact string, wantBool bool, wantString string) bool {
	v, ok := vars[fact]
	if !ok {
		return false
	}
	if wantString != "" {
		// An integer fact compares by value; the loader keeps integer cells as their decimal text.
		if n, isInt := v.(int); isInt {
			return strconv.Itoa(n) == wantString
		}
		return flowFactString(vars, fact) == wantString
	}
	b, ok := v.(bool)
	return ok && b == wantBool
}

func flowFactPresent(vars map[string]any, fact string) bool {
	v, ok := vars[fact]
	if !ok {
		return false
	}
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t) != ""
	case bool:
		return t
	case int:
		return t != 0
	default:
		return false
	}
}

func flowFactString(vars map[string]any, fact string) string {
	v, ok := vars[fact]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(s)
}

func flowFactInt(vars map[string]any, fact string) (int, bool) {
	v, ok := vars[fact]
	if !ok {
		return 0, false
	}
	n, ok := v.(int)
	return n, ok
}

func resolveFlowOutput(out FlowOutput, vars map[string]any) (string, error) {
	if uf := strings.TrimSpace(out.UseFact); uf != "" {
		id := flowFactString(vars, uf)
		if id == "" {
			return "", fmt.Errorf("use_fact %q is empty", uf)
		}
		return id, nil
	}
	if id := strings.TrimSpace(out.SurfaceID); id != "" {
		return id, nil
	}
	dispatchOn := strings.TrimSpace(out.DispatchOn)
	if dispatchOn == "" {
		return "", fmt.Errorf("output has no surface")
	}
	key := flowFactString(vars, dispatchOn)
	if key == "" {
		key = "_"
	}
	if id, ok := out.Cases[key]; ok {
		return strings.TrimSpace(id), nil
	}
	def := strings.TrimSpace(out.Default)
	if def == "" {
		return "", fmt.Errorf("dispatch_on %q: no case for %q and no default", dispatchOn, key)
	}
	return def, nil
}
