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
		return false, raise("[OAR-FACT-4] when must evaluate to bool")
	}
	return result, nil
}

// Fact reads an observation with the declared type and absent-value semantics.
func (r *Rule) Fact(name string, facts map[string]any) (any, error) {
	decl, ok := r.environment.Facts[name]
	if !ok {
		return nil, raise("[OAR-FACT-26] undeclared fact %s", name)
	}
	raw, present := facts[name]
	if !present {
		return zeroValue(decl.Type), nil
	}
	return coerce(raw, decl.Type, name)
}

// MatchSelector validates each selected observation before membership matching.
func (r *Rule) MatchSelector(facts map[string]any) (bool, error) {
	for _, clause := range r.Selector {
		name, want := clause.Fact, clause.Values
		v, err := r.Fact(name, facts)
		if err != nil {
			return false, err
		}
		matched := false
		switch actual := v.(type) {
		case string:
			for _, item := range want {
				if actual == item {
					matched = true
					break
				}
			}
		case []string:
			for _, item := range want {
				for _, value := range actual {
					if value == item {
						matched = true
						break
					}
				}
			}
		}
		if !matched {
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

// CounterReferences reads counter targets from syntax, including decoded string literals.
func CounterReferences(source, namespace string) ([]CounterRef, error) {
	if strings.TrimSpace(source) == "" {
		return nil, nil
	}
	root, err := parseCondition(source)
	if err != nil {
		return nil, err
	}
	var references []CounterRef
	walkNodes(root, func(n *node) {
		if n.kind != nodeCall {
			return
		}
		counter, reader := counterReaders[n.strValue]
		if !reader {
			return
		}
		if len(n.items) != 1 || n.items[0].kind != nodeString {
			err = loadErr("[OAR-FIRE-11] %s requires a string literal rule id", n.strValue)
			return
		}
		literal := n.items[0].strValue
		references = append(references, CounterRef{Fn: n.strValue, Counter: counter, Literal: literal, Target: ResolveRef(literal, namespace)})
	})
	return references, err
}
