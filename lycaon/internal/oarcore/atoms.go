package oarcore

import (
	"strconv"
	"strings"
)

// Truth is an atom's value in one evaluation. An atom whose evaluation raises
// is Unknown.
type Truth int8

const (
	Unknown Truth = iota
	False
	True
)

func truthOf(b bool) Truth {
	if b {
		return True
	}
	return False
}

// AtomValue is one atom of a condition with its value in one evaluation.
type AtomValue struct {
	Atom  string
	Value Truth
}

// Atoms lists the Boolean leaves of the rule's condition in source order: each
// comparison, membership test, reference, literal, call or ternary. Negation,
// && and || are connectives. Repeated equal leaves share one canonical text.
func (r *Rule) Atoms() []string {
	var out []string
	for _, n := range atomNodes(r.When) {
		out = append(out, formatNode(n))
	}
	return out
}

// EvaluateAtoms evaluates every atom on its own against the facts one
// occurrence publishes. Evaluation has no side effects, so atoms a
// short-circuit skipped can be read too.
func (r *Rule) EvaluateAtoms(facts map[string]any, functions map[string]func(any) (any, error)) ([]AtomValue, error) {
	ctx, err := r.evalContext(facts, functions)
	if err != nil {
		return nil, err
	}
	nodes := atomNodes(r.When)
	out := make([]AtomValue, 0, len(nodes))
	for _, n := range nodes {
		av := AtomValue{Atom: formatNode(n)}
		if got, err := evalNode(n, ctx); err == nil {
			if b, ok := got.(bool); ok {
				av.Value = truthOf(b)
			}
		}
		out = append(out, av)
	}
	return out, nil
}

// EvaluateConditionAtoms checks source against env and evaluates its atoms.
func EvaluateConditionAtoms(source string, env *Environment, facts map[string]any, functions map[string]func(any) (any, error)) ([]AtomValue, error) {
	if strings.TrimSpace(source) == "" {
		return nil, nil
	}
	if err := CheckCondition(source, env); err != nil {
		return nil, err
	}
	root, err := parseCondition(source)
	if err != nil {
		return nil, err
	}
	_, refs, err := checkCondition(root, env)
	if err != nil {
		return nil, err
	}
	rule := &Rule{When: root, Refs: refs, environment: env}
	return rule.EvaluateAtoms(facts, functions)
}

func atomNodes(n *node) []*node {
	if n == nil {
		return nil
	}
	switch {
	case n.kind == nodeUnary && n.op == "!":
		return atomNodes(n.left)
	case n.kind == nodeBinary && (n.op == "&&" || n.op == "||"):
		return append(atomNodes(n.left), atomNodes(n.right)...)
	}
	return []*node{n}
}

// formatNode prints n in canonical OAR syntax with every compound operand
// parenthesised.
func formatNode(n *node) string {
	if n == nil {
		return ""
	}
	switch n.kind {
	case nodeBool:
		return strconv.FormatBool(n.boolValue)
	case nodeInt:
		return strconv.FormatInt(n.intValue, 10)
	case nodeDouble:
		return CanonicalDouble(n.doubleValue)
	case nodeString:
		return strconv.Quote(n.strValue)
	case nodeIdent:
		return n.strValue
	case nodeList:
		items := make([]string, len(n.items))
		for i, it := range n.items {
			items[i] = formatNode(it)
		}
		return "[" + strings.Join(items, ", ") + "]"
	case nodeCall:
		args := make([]string, len(n.items))
		for i, it := range n.items {
			args[i] = formatNode(it)
		}
		return n.strValue + "(" + strings.Join(args, ", ") + ")"
	case nodeUnary:
		return n.op + formatOperand(n.left)
	case nodeBinary:
		return formatOperand(n.left) + " " + n.op + " " + formatOperand(n.right)
	case nodeTernary:
		return formatOperand(n.left) + " ? " + formatOperand(n.right) + " : " + formatOperand(n.third)
	}
	return ""
}

func formatOperand(n *node) string {
	if n != nil && (n.kind == nodeBinary || n.kind == nodeTernary) {
		return "(" + formatNode(n) + ")"
	}
	return formatNode(n)
}
