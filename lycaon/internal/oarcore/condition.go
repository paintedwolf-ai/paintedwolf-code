package oarcore

import (
	"sort"
	"strings"
)

// ConditionReferences extracts symbols from the parsed expression, never its literals.
func ConditionReferences(source string) ([]string, error) {
	root, err := parseCondition(source)
	if err != nil {
		return nil, err
	}
	names := map[string]bool{}
	var walk func(*node)
	walk = func(n *node) {
		if n == nil {
			return
		}
		if n.kind == nodeIdent || n.kind == nodeCall {
			names[n.strValue] = true
		}
		walk(n.left)
		walk(n.right)
		walk(n.third)
		for _, item := range n.items {
			walk(item)
		}
	}
	walk(root)
	out := make([]string, 0, len(names))
	for name := range names {
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

// FactComparison captures a comparison involving a fact and a literal string.
type FactComparison struct {
	Fact  string
	Op    string
	Value string
}

// ConditionFactComparisons extracts all fact-to-literal comparisons from when source.
func ConditionFactComparisons(source string) ([]FactComparison, error) {
	if strings.TrimSpace(source) == "" {
		return nil, nil
	}
	root, err := parseCondition(source)
	if err != nil {
		return nil, err
	}
	var comparisons []FactComparison
	walkNodes(root, func(n *node) {
		if n == nil || n.kind != nodeBinary {
			return
		}
		switch n.op {
		case "==", "!=":
			if n.left.kind == nodeIdent && n.right.kind == nodeString {
				comparisons = append(comparisons, FactComparison{
					Fact:  n.left.strValue,
					Op:    n.op,
					Value: n.right.strValue,
				})
			} else if n.right.kind == nodeIdent && n.left.kind == nodeString {
				comparisons = append(comparisons, FactComparison{
					Fact:  n.right.strValue,
					Op:    n.op,
					Value: n.left.strValue,
				})
			}
		case "in":
			if n.left.kind == nodeIdent && n.right.kind == nodeList {
				for _, item := range n.right.items {
					if item.kind == nodeString {
						comparisons = append(comparisons, FactComparison{
							Fact:  n.left.strValue,
							Op:    "in",
							Value: item.strValue,
						})
					}
				}
			} else if n.left.kind == nodeString && n.right.kind == nodeIdent {
				comparisons = append(comparisons, FactComparison{
					Fact:  n.right.strValue,
					Op:    "in",
					Value: n.left.strValue,
				})
			}
		}
	})
	return comparisons, nil
}

// ConjunctValues returns the values a condition requires fact to take: the
// literals of `fact == "v"` and `fact in [...]` among the top-level conjuncts.
// It is empty when no conjunct constrains fact that way.
func ConjunctValues(source, fact string) []string {
	if strings.TrimSpace(source) == "" {
		return nil
	}
	root, err := parseCondition(source)
	if err != nil {
		return nil
	}
	var out []string
	var walk func(*node)
	walk = func(n *node) {
		if n.kind != nodeBinary {
			return
		}
		switch {
		case n.op == "&&":
			walk(n.left)
			walk(n.right)
		case n.op == "==" && n.left.kind == nodeIdent && n.left.strValue == fact && n.right.kind == nodeString:
			out = append(out, n.right.strValue)
		case n.op == "in" && n.left.kind == nodeIdent && n.left.strValue == fact && n.right.kind == nodeList:
			for _, item := range n.right.items {
				if item.kind == nodeString {
					out = append(out, item.strValue)
				}
			}
		}
	}
	walk(root)
	return out
}

// EvaluateCondition uses the document's checked tree and capability types.
func (r *Rule) EvaluateCondition(facts map[string]any, functions map[string]func(any) (any, error)) (bool, error) {
	ctx := &evalContext{facts: map[string]value{}, functions: map[string]func(value) (value, error){}}
	for name := range r.Refs {
		if decl, ok := r.environment.Facts[name]; ok {
			v := zeroValue(decl.Type)
			if raw, present := facts[name]; present {
				var err error
				v, err = coerce(raw, decl.Type, name)
				if err != nil {
					return false, err
				}
			}
			ctx.facts[name] = v
		}
		if decl, ok := r.environment.Functions[name]; ok {
			fn := functions[name]
			ctx.functions[name] = func(arg value) (value, error) {
				if fn == nil {
					if raw, present := facts[name]; present {
						return coerce(raw, decl.Sig.Ret, name)
					}
					return zeroValue(decl.Sig.Ret), nil
				}
				raw, err := fn(arg)
				if err != nil {
					return nil, err
				}
				return coerce(raw, decl.Sig.Ret, name)
			}
		}
	}
	if r.When == nil {
		return true, nil
	}
	got, err := evalNode(r.When, ctx)
	if err != nil {
		return false, err
	}
	result, ok := got.(bool)
	if !ok {
		return false, raise("[OAR-FACT-3] when must evaluate to bool")
	}
	return result, nil
}

// Fact reads an observation with the declared type and absent-value semantics.
func (r *Rule) Fact(name string, facts map[string]any) (any, error) {
	decl, ok := r.environment.Facts[name]
	if !ok {
		return nil, raise("[OAR-FACT-18] undeclared fact %s", name)
	}
	raw, present := facts[name]
	if !present {
		return zeroValue(decl.Type), nil
	}
	return coerce(raw, decl.Type, name)
}

// MatchSelector matches the rule's selector against the occurrence: tools
// matches when the core fact tool is one of its names, and never where no tool
// is invoked ([OAR-SEL-2], [OAR-SEL-6]).
func (r *Rule) MatchSelector(facts map[string]any) (bool, error) {
	for _, clause := range r.Selector {
		v, err := r.Fact("tool", facts)
		if err != nil {
			return false, err
		}
		tool, _ := v.(string)
		if tool == "" || !contains(clause.Values, tool) {
			return false, nil
		}
	}
	return true, nil
}

// FactDeclaration exposes the type and capability dependency for set linking.
func (r *Rule) FactDeclaration(name string) (FactDecl, bool) {
	decl, ok := r.environment.Facts[name]
	return decl, ok
}
