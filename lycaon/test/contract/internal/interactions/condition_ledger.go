package interactions

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/test/contract/internal/hoststate"
	"github.com/lycaon/lycaon/test/contract/internal/independence"
)

// AtomConstraint is a production invariant over one guard atom on one
// recipient: Fixed holds it to Value, and Implies makes it imply another atom.
// Source names the production code and location that establishes it.
type AtomConstraint struct {
	Atom      string             `json:"atom"`
	Recipient string             `json:"recipient"`
	Fixed     bool               `json:"fixed,omitempty"`
	Value     independence.Truth `json:"value,omitempty"`
	Implies   string             `json:"implies,omitempty"`
	Source    string             `json:"source"`
}

// ConditionWitness is a pair of observed evaluations of one guard on one
// recipient and enclosing path in which only Atom and the decision differ.
type ConditionWitness struct {
	Site      string `json:"site"`
	Recipient string `json:"recipient"`
	Path      string `json:"path,omitempty"`
	Atom      string `json:"atom"`
	With      string `json:"with"`
	WithCase  string `json:"with_case,omitempty"`
	Then      string `json:"then"`
	ThenCase  string `json:"then_case,omitempty"`
}

// ConditionExclusion is an atom whose every independence pair the guard's
// logic or a production constraint rules out.
type ConditionExclusion struct {
	Site       string `json:"site"`
	Recipient  string `json:"recipient"`
	Atom       string `json:"atom"`
	Constraint string `json:"constraint"`
}

// ConditionGap is an atom with neither a witness nor an exclusion. Missing
// lists the assignments, other atoms fixed, that would witness it.
type ConditionGap struct {
	Site       string   `json:"site"`
	SourceSpan string   `json:"source_span"`
	Source     string   `json:"source"`
	Recipient  string   `json:"recipient"`
	Atom       string   `json:"atom"`
	Missing    []string `json:"missing"`
	// Unrecomposed counts observed vectors left out of the pair search because
	// an atom was unknown or the atoms did not give the actual decision.
	Unrecomposed int `json:"unrecomposed,omitempty"`
}

func (g ConditionGap) String() string {
	return fmt.Sprintf("%s (%s) on %s: atom %q of %q has no independent witness; observe %s",
		g.Site, g.SourceSpan, g.Recipient, g.Atom, g.Source, strings.Join(g.Missing, " or "))
}

// ConditionLedger is the independence coverage of every compound guard.
type ConditionLedger struct {
	Sites            []ConditionSite      `json:"sites"`
	CompoundSites    int                  `json:"compound_sites"`
	Obligations      int                  `json:"obligations"`
	Witnesses        []ConditionWitness   `json:"witnesses"`
	Exclusions       []ConditionExclusion `json:"exclusions,omitempty"`
	Gaps             []ConditionGap       `json:"gaps,omitempty"`
	StaleConstraints []string             `json:"stale_constraints,omitempty"`
	// HandledErrors are condition errors the rule's on_error turns into an
	// outcome; EvaluatorErrors are the rest.
	HandledErrors   []string `json:"handled_errors,omitempty"`
	EvaluatorErrors []string `json:"evaluator_errors,omitempty"`
}

// BuildConditionLedger joins discovered guards with their observed
// evaluations and, per guard, recipient and atom, finds a witness pair or the
// constraint that excludes one. Observations are grouped by recipient and
// enclosing include path; only vectors whose atoms recompose the actual
// decision take part. A compound guard nobody evaluated is a gap for every
// atom.
func BuildConditionLedger(sites []ConditionSite, observed []hoststate.ObservedCondition, constraints []AtomConstraint) (*ConditionLedger, error) {
	byID := map[string]ConditionSite{}
	for _, s := range sites {
		byID[s.ID] = s
	}
	obsByID := map[string]hoststate.ObservedCondition{}
	for _, o := range observed {
		obsByID[o.Site] = o
		if _, ok := byID[o.Site]; !ok {
			byID[o.Site] = ConditionSite{ID: o.Site, Family: o.Family, SourceSpan: o.Site, Source: o.Source, Atoms: o.Atoms}
		}
	}
	l := &ConditionLedger{}
	for _, id := range sortedKeysOf(boolKeys(byID)) {
		site := byID[id]
		l.Sites = append(l.Sites, site)
		obs, seen := obsByID[id]
		for _, e := range obs.Errors {
			if obs.ErrorsHandled {
				l.HandledErrors = append(l.HandledErrors, id+": "+e)
			} else {
				l.EvaluatorErrors = append(l.EvaluatorErrors, id+": "+e)
			}
		}
		atoms := site.Atoms
		if seen && len(obs.Atoms) > 0 {
			atoms = obs.Atoms
		}
		inputs := distinct(atoms)
		if len(inputs) < 2 {
			continue
		}
		l.CompoundSites++
		if !seen || len(obs.Vectors) == 0 {
			for _, in := range inputs {
				l.Obligations++
				l.Gaps = append(l.Gaps, ConditionGap{Site: id, SourceSpan: site.SourceSpan, Source: site.Source, Recipient: "any", Atom: in, Missing: []string{"any evaluation"}})
			}
			continue
		}
		if obs.Decide == nil {
			return nil, fmt.Errorf("%s: no decision function for %q", id, obs.Source)
		}
		if err := l.analyzeSite(site, obs, inputs, constraints); err != nil {
			return nil, err
		}
	}
	slices.SortFunc(l.Gaps, func(a, b ConditionGap) int {
		return cmp.Or(strings.Compare(a.Site, b.Site), strings.Compare(a.Recipient, b.Recipient), strings.Compare(a.Atom, b.Atom))
	})
	return l, nil
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

// analyzeSite runs the pair search for each recipient that evaluated obs;
// vectors pair only within one enclosing include path.
func (l *ConditionLedger) analyzeSite(site ConditionSite, obs hoststate.ObservedCondition, inputs []string, constraints []AtomConstraint) error {
	guards := map[string]*independence.Guard{}
	unrecomposed := map[string]int{}
	for _, v := range obs.Vectors {
		g := guards[v.Partition]
		if g == nil {
			g = &independence.Guard{Site: site.ID, Source: obs.Source, Atoms: obs.Atoms}
			guards[v.Partition] = g
		}
		if !v.Recomposes {
			unrecomposed[v.Partition]++
			continue
		}
		g.Vectors = append(g.Vectors, independence.Vector{Atoms: v.Atoms, Decision: v.Decision, Group: strings.Join(v.Path, ">"), Case: v.Case})
	}
	eval := func(_ string, truths []independence.Truth) (bool, error) {
		values := make(map[string]bool, len(truths))
		for i, t := range truths {
			values[obs.Atoms[i]] = t == independence.True
		}
		return obs.Decide(values), nil
	}
	for _, recipient := range sortedKeysOf(boolKeys(guards)) {
		res, err := independence.Analyze([]independence.Guard{*guards[recipient]}, eval, constraintsFor(constraints, recipient, inputs))
		if err != nil {
			return fmt.Errorf("%s on %s: %w", site.ID, recipient, err)
		}
		l.StaleConstraints = append(l.StaleConstraints, prefixed(site.ID+" on "+recipient+": ", res.Contradicted)...)
		witnessed := map[string]independence.Witness{}
		for _, w := range res.Witnesses[site.ID] {
			witnessed[w.Input] = w
		}
		for _, in := range inputs {
			l.Obligations++
			if w, ok := witnessed[in]; ok {
				l.Witnesses = append(l.Witnesses, ConditionWitness{
					Site: site.ID, Recipient: recipient, Path: w.With.Group, Atom: in,
					With: formatVector(obs.Atoms, w.With), WithCase: w.With.Case,
					Then: formatVector(obs.Atoms, w.Then), ThenCase: w.Then.Case,
				})
				continue
			}
			if i := slices.IndexFunc(res.Exclusions, func(ex independence.Exclusion) bool { return ex.Input == in }); i >= 0 {
				l.Exclusions = append(l.Exclusions, ConditionExclusion{Site: site.ID, Recipient: recipient, Atom: in, Constraint: res.Exclusions[i].Reason})
				continue
			}
			gap := ConditionGap{Site: site.ID, SourceSpan: site.SourceSpan, Source: site.Source, Recipient: recipient, Atom: in, Unrecomposed: unrecomposed[recipient]}
			if i := slices.IndexFunc(res.Gaps, func(g independence.Gap) bool { return g.Input == in }); i >= 0 {
				gap.Missing = res.Gaps[i].Missing
			}
			l.Gaps = append(l.Gaps, gap)
		}
	}
	return nil
}

func constraintsFor(all []AtomConstraint, recipient string, inputs []string) independence.Constraints {
	var out independence.Constraints
	for _, c := range all {
		if c.Recipient != recipient || !slices.Contains(inputs, c.Atom) {
			continue
		}
		if c.Fixed {
			out.Fixed = append(out.Fixed, independence.Fixed{Atom: c.Atom, Value: c.Value, Source: c.Source})
		}
		if c.Implies != "" && slices.Contains(inputs, c.Implies) {
			out.Implications = append(out.Implications, independence.Implication{If: c.Atom, Then: c.Implies, Source: c.Source})
		}
	}
	return out
}

func formatVector(atoms []string, v independence.Vector) string {
	parts := make([]string, len(atoms))
	for i, a := range atoms {
		parts[i] = a + "=" + v.Atoms[i].String()
	}
	return fmt.Sprintf("{%s} -> %t", strings.Join(parts, ", "), v.Decision)
}

func prefixed(prefix string, in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = prefix + s
	}
	return out
}

// ValidateZeroUnexplainedGaps fails on any atom without a witness or
// exclusion, and on any constraint an observation contradicts, naming the
// site, atom, recipient and missing vectors.
func (l *ConditionLedger) ValidateZeroUnexplainedGaps() error {
	var issues []string
	for _, g := range l.Gaps {
		issues = append(issues, g.String())
	}
	for _, s := range l.StaleConstraints {
		issues = append(issues, "stale constraint: "+s)
	}
	for _, e := range l.EvaluatorErrors {
		issues = append(issues, "condition raised without on_error: "+e)
	}
	if len(issues) == 0 {
		return nil
	}
	return fmt.Errorf("condition independence gaps (%d):\n%s", len(issues), strings.Join(issues, "\n"))
}
