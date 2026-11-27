package casegen

import (
	"fmt"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/test/contract/internal/independence"
)

// Logic evaluates a guard's atoms on their own against typed facts with the
// production evaluator.
type Logic interface {
	// References lists the declared facts atom reads.
	References(atom string) []string
	// Eval gives atom's truth when the facts hold the given values and every
	// other fact its zero value.
	Eval(atom string, facts map[string]any) independence.Truth
	// Domain returns values of fact that between them reach every truth
	// combination atoms can take over it, or false when no such finite set
	// is known.
	Domain(fact string, atoms []string) ([]any, bool)
	// Zero is the declared zero value of fact.
	Zero(fact string) any
}

// soleFact returns the one fact atom reads.
func soleFact(l Logic, atom string) (string, bool) {
	refs := l.References(atom)
	if len(refs) != 1 {
		return "", false
	}
	return refs[0], true
}

// SharedFactJoints constrains atoms that read one fact and nothing else: the
// truth vectors its domain realises are the only ones that can occur. A
// fact with one such atom, an incomplete domain, or a domain realising every
// vector yields nothing.
func SharedFactJoints(atoms []string, l Logic) []independence.Joint {
	byFact := map[string][]string{}
	for _, a := range distinct(atoms) {
		if f, ok := soleFact(l, a); ok {
			byFact[f] = append(byFact[f], a)
		}
	}
	var out []independence.Joint
	for _, fact := range sortedKeys(byFact) {
		group := byFact[fact]
		if len(group) < 2 {
			continue
		}
		values, ok := l.Domain(fact, group)
		if !ok {
			continue
		}
		var allowed [][]independence.Truth
		for _, v := range values {
			vec := make([]independence.Truth, len(group))
			for i, a := range group {
				vec[i] = l.Eval(a, map[string]any{fact: v})
			}
			if !slices.ContainsFunc(allowed, func(x []independence.Truth) bool { return slices.Equal(x, vec) }) {
				allowed = append(allowed, vec)
			}
		}
		if len(allowed) == 1<<len(group) {
			continue
		}
		out = append(out, independence.Joint{
			Atoms: group, Allowed: allowed,
			Source: fmt.Sprintf("expression: %s read only %s, whose values %v realise only %s", strings.Join(group, "; "), fact, values, formatVectors(allowed)),
		})
	}
	return out
}

// FixedFacts holds each atom that reads only facts in fixed at its truth
// under those values.
func FixedFacts(atoms []string, l Logic, fixed map[string]any, source string) []independence.Fixed {
	var out []independence.Fixed
	for _, a := range distinct(atoms) {
		refs := l.References(a)
		if len(refs) == 0 || slices.ContainsFunc(refs, func(f string) bool { _, ok := fixed[f]; return !ok }) {
			continue
		}
		if v := l.Eval(a, fixed); v != independence.Unknown {
			out = append(out, independence.Fixed{Atom: a, Value: v, Source: source})
		}
	}
	return out
}

// ZeroUnless encodes a producer coupling: unless the gate atom holds, every
// fact in facts keeps its zero value, so an atom reading only those facts
// takes its zero-value truth whenever the gate is false.
func ZeroUnless(atoms []string, l Logic, gate string, facts []string, source string) []independence.Joint {
	if !slices.Contains(atoms, gate) {
		return nil
	}
	zero := map[string]any{}
	for _, f := range facts {
		zero[f] = l.Zero(f)
	}
	var out []independence.Joint
	for _, a := range distinct(atoms) {
		refs := l.References(a)
		if a == gate || len(refs) == 0 || slices.ContainsFunc(refs, func(f string) bool { return !slices.Contains(facts, f) }) {
			continue
		}
		z := l.Eval(a, zero)
		if z == independence.Unknown {
			continue
		}
		out = append(out, independence.Joint{
			Atoms: []string{a, gate},
			Allowed: [][]independence.Truth{
				{z, independence.False}, {independence.True, independence.True}, {independence.False, independence.True},
			},
			Source: fmt.Sprintf("%s: %s is %s unless %s", source, a, z, gate),
		})
	}
	return out
}

func formatVectors(vs [][]independence.Truth) string {
	parts := make([]string, len(vs))
	for i, v := range vs {
		var b strings.Builder
		for _, t := range v {
			b.WriteString(t.String())
		}
		parts[i] = b.String()
	}
	return "[" + strings.Join(parts, " ") + "]"
}

func distinct(atoms []string) []string {
	var out []string
	for _, a := range atoms {
		if !slices.Contains(out, a) {
			out = append(out, a)
		}
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
