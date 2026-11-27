package oarproducers

import (
	"fmt"
	"maps"
	"slices"
	"strings"

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
	return &ruleLogic{env: p.env, selector: rule.Tools(), compiled: p.compiled}
}

// Anchor names the anchor site's rule fires at.
func (p *CaseProducer) Anchor(site string) string {
	if rule, _ := p.ruleOf(site); rule != nil {
		return rule.Anchor
	}
	return ""
}

// WhenReferences lists the declared facts the condition of site's rule reads.
func (p *CaseProducer) WhenReferences(site string) []string {
	rule, _ := p.ruleOf(site)
	if rule == nil {
		return nil
	}
	return p.references(rule.When)
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

// compile checks source once.
func (l *ruleLogic) compile(source string) *oarcore.Rule {
	r, ok := l.compiled[source]
	if !ok {
		r, _ = oarcore.CompileCondition(source, l.env)
		l.compiled[source] = r
	}
	return r
}

func (l *ruleLogic) Eval(atom string, facts map[string]any) independence.Truth {
	r := l.compile(atom)
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
	default:
		return nil, false
	}
}

func (l *ruleLogic) Zero(fact string) any {
	switch l.env.Facts[fact].Type {
	case oarcore.TypeBool:
		return false
	case oarcore.TypeInt:
		return int64(0)
	case oarcore.TypeListString:
		return []string{}
	default:
		return ""
	}
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

// maxShadowInputs caps the guards whose vectors ShadowJoints enumerates.
const maxShadowInputs = 14

// ShadowJoints rules out the vectors of a rule's condition under which a
// mandatory block rule evaluated earlier on the same anchor fires: the first
// enforced block stops evaluation ([OAR-EVAL-3]), so the rule never sees them.
// A rule that is not mandatory can be disabled ([OAR-OPS-5] refuses only
// mandatory disables), so it shadows nothing for good; Shadowers names those
// rules, and the vectors they hide are generated with them disabled.
func (p *CaseProducer) ShadowJoints(site string, atoms []string) []independence.Joint {
	return p.shadowing(site, atoms).joints
}

// Shadowers lists the disableable block rules that hide some vector of
// site's condition on the stock catalog.
func (p *CaseProducer) Shadowers(site string) []string {
	rule, src := p.ruleOf(site)
	if rule == nil {
		return nil
	}
	compiled := (&ruleLogic{env: p.env, compiled: p.compiled}).compile(src)
	if compiled == nil {
		return nil
	}
	return p.shadowing(site, compiled.Atoms()).disableable
}

type shadowResult struct {
	joints      []independence.Joint
	disableable []string
}

func (p *CaseProducer) shadowing(site string, atoms []string) shadowResult {
	if r, ok := p.shadows[site]; ok {
		return r
	}
	r := p.computeShadowing(site, atoms)
	p.shadows[site] = r
	return r
}

func (p *CaseProducer) computeShadowing(site string, atoms []string) shadowResult {
	rule, _ := p.ruleOf(site)
	inputs := distinctOf(atoms)
	if rule == nil || !strings.HasSuffix(site, "#when") || len(inputs) > maxShadowInputs {
		return shadowResult{}
	}
	logic := &ruleLogic{env: p.env, selector: rule.Tools(), compiled: p.compiled}
	var earlier []*oar.Rule
	for _, r := range p.stock.Rules.OnAnchor(rule.Anchor) {
		if r == rule {
			break
		}
		if r.Effect == oar.EffectBlock && r.Enforcement != "monitor" && selects(r.Tools(), rule.Tools()) {
			earlier = append(earlier, r)
		}
	}
	var allowed [][]independence.Truth
	mandatory, disableable := map[string]bool{}, map[string]bool{}
	for mask := 0; mask < 1<<len(inputs); mask++ {
		vec := make([]independence.Truth, len(inputs))
		for i := range inputs {
			vec[i] = independence.False
			if mask&(1<<i) != 0 {
				vec[i] = independence.True
			}
		}
		shadowed := false
		for _, r := range earlier {
			if !fires(logic, r, inputs, vec) {
				continue
			}
			if r.Mandatory {
				shadowed, mandatory[r.ID] = true, true
				break
			}
			disableable[r.ID] = true
		}
		if !shadowed {
			allowed = append(allowed, vec)
		}
	}
	out := shadowResult{disableable: slices.Sorted(maps.Keys(disableable))}
	if len(mandatory) > 0 {
		out.joints = []independence.Joint{{Atoms: inputs, Allowed: allowed,
			Source: fmt.Sprintf("OAR-EVAL-3 with OAR-OPS-5: the mandatory rules %v on %s evaluate before %s, cannot be disabled, and block under the excluded vectors", slices.Sorted(maps.Keys(mandatory)), rule.Anchor, rule.ID)}}
	}
	return out
}

// selects reports that a rule with selector outer is evaluated for every
// tool a rule with selector inner admits.
func selects(outer, inner []string) bool {
	if len(outer) == 0 {
		return true
	}
	return len(inner) > 0 && !slices.ContainsFunc(inner, func(t string) bool { return !slices.Contains(outer, t) })
}

// fires reports that r's condition holds under every fact assignment that
// gives inputs the truths vec.
func fires(logic *ruleLogic, r *oar.Rule, inputs []string, vec []independence.Truth) bool {
	compiled := logic.compile(r.When)
	if compiled == nil {
		return false
	}
	known := map[string]bool{}
	var open []string
	for _, a := range distinctOf(compiled.Atoms()) {
		if i := slices.Index(inputs, a); i >= 0 {
			known[a] = vec[i] == independence.True
			continue
		}
		t, ok := impliedTruth(logic, a, inputs, vec)
		switch {
		case ok:
			known[a] = t
		case len(open) == 3:
			return false
		default:
			open = append(open, a)
		}
	}
	for mask := 0; mask < 1<<len(open); mask++ {
		for i, a := range open {
			known[a] = mask&(1<<i) != 0
		}
		if !compiled.DecideFromAtoms(known) {
			return false
		}
	}
	return true
}

// impliedTruth gives atom's truth when it reads one fact that inputs also
// read alone and every value of that fact matching vec gives it one truth.
func impliedTruth(logic *ruleLogic, atom string, inputs []string, vec []independence.Truth) (bool, bool) {
	refs := logic.References(atom)
	if len(refs) != 1 {
		return false, false
	}
	var group []string
	var want []independence.Truth
	for i, in := range inputs {
		if slices.Equal(logic.References(in), refs) {
			group, want = append(group, in), append(want, vec[i])
		}
	}
	if len(group) == 0 {
		return false, false
	}
	values, ok := logic.Domain(refs[0], append(slices.Clone(group), atom))
	if !ok {
		return false, false
	}
	seen := map[independence.Truth]bool{}
	for _, v := range values {
		facts := map[string]any{refs[0]: v}
		match := true
		for i, g := range group {
			if logic.Eval(g, facts) != want[i] {
				match = false
				break
			}
		}
		if match {
			seen[logic.Eval(atom, facts)] = true
		}
	}
	if len(seen) != 1 || seen[independence.Unknown] {
		return false, false
	}
	return seen[independence.True], true
}
