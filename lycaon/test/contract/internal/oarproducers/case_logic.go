package oarproducers

import (
	"fmt"
	"slices"

	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/oarcore"
	"github.com/lycaon/lycaon/test/contract/internal/casegen"
	"github.com/lycaon/lycaon/test/contract/internal/independence"
)

// ruleLogic evaluates one rule's guard atoms with the production OAR
// evaluator. A rule with a tool selector is evaluated only for the tools it
// admits, so those are the domain of tool.
type ruleLogic struct {
	env      *oarcore.Environment
	selector []string
	compiled map[string]*oarcore.Rule
}

// Logic returns the atom evaluator for site's rule, or nil for an unknown
// site.
func (p *CaseProducer) Logic(site string) casegen.Logic {
	rule, _ := p.ruleOf(site)
	if rule == nil {
		return nil
	}
	return &ruleLogic{env: p.env, selector: rule.Tools(), compiled: map[string]*oarcore.Rule{}}
}

// Anchor names the anchor site's rule fires at.
func (p *CaseProducer) Anchor(site string) string {
	if rule, _ := p.ruleOf(site); rule != nil {
		return rule.Anchor
	}
	return ""
}

// Decide gives site's decision for atom truths keyed by atom text.
func (p *CaseProducer) Decide(site string) func(map[string]bool) bool {
	_, src := p.ruleOf(site)
	compiled, err := oarcore.CompileCondition(src, p.env)
	if err != nil {
		return nil
	}
	return compiled.DecideFromAtoms
}

func (l *ruleLogic) References(atom string) []string {
	refs, _ := oarcore.ConditionReferences(atom)
	return slices.DeleteFunc(refs, func(r string) bool { _, ok := l.env.Facts[r]; return !ok })
}

func (l *ruleLogic) Eval(atom string, facts map[string]any) independence.Truth {
	r, ok := l.compiled[atom]
	if !ok {
		r, _ = oarcore.CompileCondition(atom, l.env)
		l.compiled[atom] = r
	}
	if r == nil {
		return independence.Unknown
	}
	held, err := r.EvaluateCondition(facts, nil)
	switch {
	case err != nil:
		return independence.Unknown
	case held:
		return independence.True
	}
	return independence.False
}

// Domain covers every truth combination atoms can take over fact when each
// atom compares fact with literals only: the literals plus a value equal to
// none of them for a string, every subset of the literals for a list tested
// by membership, and the selector's tools for tool.
func (l *ruleLogic) Domain(fact string, atoms []string) ([]any, bool) {
	var lits []string
	for _, a := range atoms {
		refs, _ := oarcore.ConditionReferences(a)
		cmps, _ := oarcore.ConditionFactComparisons(a)
		if !slices.Equal(refs, []string{fact}) || len(cmps) == 0 {
			return nil, false
		}
		for _, c := range cmps {
			if !slices.Contains(lits, c.Value) {
				lits = append(lits, c.Value)
			}
		}
	}
	if fact == "tool" && len(l.selector) > 0 {
		out := make([]any, len(l.selector))
		for i, t := range l.selector {
			out[i] = t
		}
		return out, true
	}
	switch l.env.Facts[fact].Type {
	case oarcore.TypeString:
		out := []any{"", "\x00none-of-the-literals"}
		for _, v := range lits {
			out = append(out, v)
		}
		return out, true
	case oarcore.TypeListString:
		if len(lits) > 4 {
			return nil, false
		}
		var out []any
		for mask := 0; mask < 1<<len(lits); mask++ {
			set := []string{}
			for i, v := range lits {
				if mask&(1<<i) != 0 {
					set = append(set, v)
				}
			}
			out = append(out, set)
		}
		return out, true
	}
	return nil, false
}

func (l *ruleLogic) Zero(fact string) any {
	switch l.env.Facts[fact].Type {
	case oarcore.TypeBool:
		return false
	case oarcore.TypeInt:
		return int64(0)
	case oarcore.TypeListString:
		return []string{}
	}
	return ""
}

// toolFacts are the facts the producer derives from the invoked tool alone.
var toolFacts = map[string]bool{
	"tool": true, "paintedwolf.tool_is_task": true, "paintedwolf.tool_is_state": true, "paintedwolf.tool_is_delegation": true,
	"paintedwolf.tool_is_handoff": true, "paintedwolf.tool_payload_chunkable": true, "paintedwolf.receiving_tool_summarize": true,
}

// SelectorFixed holds each atom that reads only tool-derived facts at its
// truth when every tool the rule's selector admits gives it the same truth:
// the pipeline evaluates the rule for those tools only.
func (p *CaseProducer) SelectorFixed(site string, atoms []string) []independence.Fixed {
	rule, _ := p.ruleOf(site)
	if rule == nil || len(rule.Tools()) == 0 {
		return nil
	}
	logic := p.Logic(site)
	var out []independence.Fixed
	for _, a := range distinctOf(atoms) {
		refs := logic.References(a)
		if len(refs) == 0 || slices.ContainsFunc(refs, func(f string) bool { return !toolFacts[f] }) {
			continue
		}
		truth := independence.Unknown
		for _, tool := range rule.Tools() {
			gc := oar.NewGuardContext()
			gc.ObserveToolCall(tool, nil)
			gc.DeriveToolClassFacts()
			held, err := oar.EvaluateCondition(a, gc)
			t := independence.False
			if held {
				t = independence.True
			}
			if err != nil || (truth != independence.Unknown && t != truth) {
				truth = independence.Unknown
				break
			}
			truth = t
		}
		if truth != independence.Unknown {
			out = append(out, independence.Fixed{Atom: a, Value: truth,
				Source: fmt.Sprintf("policy/%s selector admits only %v, and GuardContext.DeriveToolClassFacts derives the tool facts from the tool", rule.ID, rule.Tools())})
		}
	}
	return out
}

func distinctOf(atoms []string) []string {
	var out []string
	for _, a := range atoms {
		if !slices.Contains(out, a) {
			out = append(out, a)
		}
	}
	return out
}
