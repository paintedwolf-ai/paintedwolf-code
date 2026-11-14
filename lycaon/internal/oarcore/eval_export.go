package oarcore

import "strings"

// CheckCondition type-checks source against env.
func CheckCondition(source string, env *Environment) error {
	if strings.TrimSpace(source) == "" {
		return nil
	}
	ast, err := parseCondition(source)
	if err != nil {
		return err
	}
	if env != nil && env.ExpressionNodesMax > 0 {
		if n := countNodes(ast); n > env.ExpressionNodesMax {
			return loadErr("[OAR-EXPR-19] condition has %d parse-tree nodes, above this engine's limit of %d", n, env.ExpressionNodesMax)
		}
	}
	typ, _, err := checkCondition(ast, env)
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
	if err := CheckCondition(source, env); err != nil {
		return false, err
	}
	root, err := parseCondition(source)
	if err != nil {
		return false, err
	}
	_, refs, err := checkCondition(root, env)
	if err != nil {
		return false, err
	}
	rule := &Rule{When: root, Refs: refs, environment: env}
	return rule.EvaluateCondition(facts, functions)
}
