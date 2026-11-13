package oarcore

import "strings"

// CheckCondition type-checks source against env (–).
// reachable is every name the rule may mention; pass every declared fact when
// the caller already enforces `requires` separately.
func CheckCondition(source string, env *Environment, reachable map[string]bool) error {
	if strings.TrimSpace(source) == "" {
		return nil
	}
	ast, err := parseCondition(source)
	if err != nil {
		return err
	}
	if env != nil && env.ExpressionNodesMax > 0 {
		if n := countNodes(ast); n > env.ExpressionNodesMax {
			return loadErr("condition has %d parse-tree nodes, above the declared expression_nodes_max of %d", n, env.ExpressionNodesMax)
		}
	}
	typ, _, err := checkCondition(ast, env, reachable)
	if err != nil {
		return err
	}
	if typ != TypeBool {
		return loadErr("[OAR-FACT-3] when must have type bool")
	}
	return nil
}

// EvaluateCondition parses and evaluates a condition against the facts and
// observation functions one occurrence publishes. Division or modulo by zero
// raises; an absent map key or out-of-range index raises;
// 64-bit integer overflow raises.
func EvaluateCondition(source string, env *Environment, facts map[string]any, functions map[string]func(any) (any, error)) (bool, error) {
	if strings.TrimSpace(source) == "" {
		return true, nil
	}
	reachable := map[string]bool{}
	for name := range env.Facts {
		reachable[name] = true
	}
	for name := range env.Functions {
		reachable[name] = true
	}
	if err := CheckCondition(source, env, reachable); err != nil {
		return false, err
	}
	root, err := parseCondition(source)
	if err != nil {
		return false, err
	}
	_, refs, err := checkCondition(root, env, reachable)
	if err != nil {
		return false, err
	}
	rule := &Rule{When: root, Refs: refs, environment: env}
	return rule.EvaluateCondition(facts, functions)
}

// ValidateTransformTarget checks that a transform target is either the whole
// content or a list<map> fact whose members are well-formed spans.
// A malformed span is a rule that cannot be
// evaluated.
func ValidateTransformTarget(target string, facts map[string]any, contentLen int) error {
	if target == "content" {
		return nil
	}
	_, err := readSpans(facts[target], contentLen)
	return err
}
