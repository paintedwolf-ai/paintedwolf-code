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

// Dimensions are the selector dimensions a host models beyond the core
// facts, by clause name. A present key with a nil value is an unbound
// dimension; an absent key is one the host does not model.
type Dimensions map[string]any

// coreDimensions read the core fact each clause names ([OAR-SEL-5]).
var coreDimensions = map[string]string{
	"tool": "tool", "tools": "tool", "profiles": "profile", "session_posture": "session_posture",
}

// MatchSelector matches every clause against the occurrence ([OAR-SEL-2]). A
// clause over a dimension the host does not model does not match
// ([OAR-SEL-6]).
func (r *Rule) MatchSelector(facts map[string]any, dims Dimensions) (bool, error) {
	for _, clause := range r.Selector {
		ok, err := r.matchClause(clause, facts, dims)
		if err != nil || !ok {
			return false, err
		}
	}
	return true, nil
}

func (r *Rule) matchClause(clause SelectorClause, facts map[string]any, dims Dimensions) (bool, error) {
	if clause.Dimension == "ledger" {
		for name, want := range clause.Ledger {
			if _, declared := r.environment.Facts[name]; !declared {
				return false, nil
			}
			v, err := r.Fact(name, facts)
			if err != nil {
				return false, err
			}
			if !memberOf(v, want) {
				return false, nil
			}
		}
		return true, nil
	}
	var actual value
	if fact, core := coreDimensions[clause.Dimension]; core {
		v, err := r.Fact(fact, facts)
		if err != nil {
			return false, err
		}
		actual = v
	} else {
		v, modeled := dims[clause.Dimension]
		if !modeled {
			return false, nil
		}
		actual = v
	}
	if clause.Null {
		return actual == nil || actual == "", nil
	}
	return memberOf(actual, clause.Values), nil
}

// memberOf reports whether a string value is one of want, or a list value
// shares a member with it.
func memberOf(v value, want []string) bool {
	switch actual := v.(type) {
	case string:
		for _, w := range want {
			if actual == w {
				return true
			}
		}
	case []string:
		for _, a := range actual {
			for _, w := range want {
				if a == w {
					return true
				}
			}
		}
	}
	return false
}

// FactDeclaration exposes the type and capability dependency for set linking.
func (r *Rule) FactDeclaration(name string) (FactDecl, bool) {
	decl, ok := r.environment.Facts[name]
	return decl, ok
}
