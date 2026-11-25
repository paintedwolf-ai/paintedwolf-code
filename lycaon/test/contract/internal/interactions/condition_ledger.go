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
	// Needs are the first missing assignments as structured case-generation
	// targets.
	Needs []map[string]independence.Truth `json:"-"`
	// Unrecomposed counts observed vectors left out of the pair search because
	// an atom was unknown or the atoms did not give the actual decision.
	Unrecomposed int `json:"unrecomposed,omitempty"`
	// Search is why targeted case generation reached no witness.
	Search string `json:"search,omitempty"`
}

func (g ConditionGap) String() string {
	s := fmt.Sprintf("%s (%s) on %s: atom %q of %q has no independent witness; observe %s",
		g.Site, g.SourceSpan, g.Recipient, g.Atom, g.Source, strings.Join(g.Missing, " or "))
	if g.Search != "" {
		s += "; generation: " + g.Search
	}
	return s
}

// SearchKey names one gap's generation outcome in LedgerInput.Searches.
func SearchKey(site, recipient, atom string) string {
	return site + "\x00" + recipient + "\x00" + atom
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
	// Observed counts the observed guard vectors by family and by recipient.
	ObservedFamilies   map[string]int    `json:"observed_families"`
	ObservedRecipients map[string]int    `json:"observed_recipients"`
	Generation         GenerationSummary `json:"generation"`
}

// GenerationSummary counts the targeted cases generated for gaps: targets
// searched, targets that reached a witnessing pair, and producer runs.
type GenerationSummary struct {
	Targets int `json:"targets"`
	Found   int `json:"found"`
	Runs    int `json:"runs"`
}

// LedgerInput is what a ledger joins.
type LedgerInput struct {
	Sites    []ConditionSite
	Observed []hoststate.ObservedCondition
	// Constraints are recorded production invariants over atoms.
	Constraints []AtomConstraint
	// Derived returns the constraints a site's expression or producer
	// establishes on a recipient.
	Derived func(site ConditionSite, recipient string) independence.Constraints
	// Searches are targeted generation diagnostics by SearchKey.
	Searches map[string]string
}

// BuildConditionLedger joins discovered guards with their observed
// evaluations and, per guard, recipient and atom, finds a witness pair or the
// constraint that excludes one. Observations are grouped by recipient and
// enclosing include path; only vectors whose atoms recompose the actual
// decision take part. A compound guard nobody evaluated is a gap for every
// atom.
func BuildConditionLedger(in LedgerInput) (*ConditionLedger, error) {
	byID := map[string]ConditionSite{}
	for _, s := range in.Sites {
		byID[s.ID] = s
	}
	obsByID := map[string]hoststate.ObservedCondition{}
	for _, o := range in.Observed {
		obsByID[o.Site] = o
		if _, ok := byID[o.Site]; !ok {
			byID[o.Site] = ConditionSite{ID: o.Site, Family: o.Family, SourceSpan: o.Site, Source: o.Source, Atoms: o.Atoms}
		}
	}
	l := &ConditionLedger{ObservedFamilies: map[string]int{}, ObservedRecipients: map[string]int{}}
	for _, o := range in.Observed {
		for _, v := range o.Vectors {
			l.ObservedFamilies[o.Family]++
			l.ObservedRecipients[v.Partition]++
		}
	}
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
			for _, atom := range inputs {
				l.Obligations++
				l.Gaps = append(l.Gaps, ConditionGap{
					Site: id, SourceSpan: site.SourceSpan, Source: site.Source, Recipient: RecipientAny, Atom: atom,
					Missing: []string{"any evaluation"}, Search: in.Searches[SearchKey(id, RecipientAny, atom)],
				})
			}
			continue
		}
		if obs.Decide == nil {
			return nil, fmt.Errorf("%s: no decision function for %q", id, obs.Source)
		}
		if err := l.analyzeSite(site, obs, inputs, in); err != nil {
			return nil, err
		}
	}
	slices.SortFunc(l.Gaps, func(a, b ConditionGap) int {
		return cmp.Or(strings.Compare(a.Site, b.Site), strings.Compare(a.Recipient, b.Recipient), strings.Compare(a.Atom, b.Atom))
	})
	return l, nil
}

// RecipientAny is the recipient of a gap at a guard nobody evaluated.
const RecipientAny = "any"

// MergeObservations joins observations of the same guard, keeping each
// distinct vector once with the case that first produced it.
func MergeObservations(sets ...[]hoststate.ObservedCondition) []hoststate.ObservedCondition {
	byID := map[string]*hoststate.ObservedCondition{}
	var order []string
	for _, set := range sets {
		for _, o := range set {
			cur := byID[o.Site]
			if cur == nil {
				c := o
				c.Vectors = nil
				c.Errors = slices.Clone(o.Errors)
				byID[o.Site], cur = &c, &c
				order = append(order, o.Site)
			} else {
				for _, e := range o.Errors {
					if !slices.Contains(cur.Errors, e) {
						cur.Errors = append(cur.Errors, e)
					}
				}
				cur.ErrorsHandled = cur.ErrorsHandled || o.ErrorsHandled
			}
			for _, v := range o.Vectors {
				if !slices.ContainsFunc(cur.Vectors, func(w hoststate.ObservedVector) bool { return sameVector(v, w) }) {
					cur.Vectors = append(cur.Vectors, v)
				}
			}
		}
	}
	out := make([]hoststate.ObservedCondition, 0, len(order))
	for _, id := range order {
		out = append(out, *byID[id])
	}
	slices.SortFunc(out, func(a, b hoststate.ObservedCondition) int { return strings.Compare(a.Site, b.Site) })
	return out
}

func sameVector(a, b hoststate.ObservedVector) bool {
	return a.Decision == b.Decision && a.Partition == b.Partition && slices.Equal(a.Atoms, b.Atoms) && slices.Equal(a.Path, b.Path)
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
func (l *ConditionLedger) analyzeSite(site ConditionSite, obs hoststate.ObservedCondition, inputs []string, in LedgerInput) error {
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
		cons := constraintsFor(in.Constraints, recipient, inputs)
		if in.Derived != nil {
			d := in.Derived(site, recipient)
			cons.Fixed = append(cons.Fixed, d.Fixed...)
			cons.Implications = append(cons.Implications, d.Implications...)
			cons.Joints = append(cons.Joints, d.Joints...)
		}
		res, err := independence.Analyze([]independence.Guard{*guards[recipient]}, eval, cons)
		if err != nil {
			return fmt.Errorf("%s on %s: %w", site.ID, recipient, err)
		}
		l.StaleConstraints = append(l.StaleConstraints, prefixed(site.ID+" on "+recipient+": ", res.Contradicted)...)
		witnessed := map[string]independence.Witness{}
		for _, w := range res.Witnesses[site.ID] {
			witnessed[w.Input] = w
		}
		for _, atom := range inputs {
			l.Obligations++
			if w, ok := witnessed[atom]; ok {
				l.Witnesses = append(l.Witnesses, ConditionWitness{
					Site: site.ID, Recipient: recipient, Path: w.With.Group, Atom: atom,
					With: formatVector(obs.Atoms, w.With), WithCase: w.With.Case,
					Then: formatVector(obs.Atoms, w.Then), ThenCase: w.Then.Case,
				})
				continue
			}
			if i := slices.IndexFunc(res.Exclusions, func(ex independence.Exclusion) bool { return ex.Input == atom }); i >= 0 {
				l.Exclusions = append(l.Exclusions, ConditionExclusion{Site: site.ID, Recipient: recipient, Atom: atom, Constraint: res.Exclusions[i].Reason})
				continue
			}
			gap := ConditionGap{
				Site: site.ID, SourceSpan: site.SourceSpan, Source: site.Source, Recipient: recipient, Atom: atom,
				Unrecomposed: unrecomposed[recipient], Search: in.Searches[SearchKey(site.ID, recipient, atom)],
			}
			if i := slices.IndexFunc(res.Gaps, func(g independence.Gap) bool { return g.Input == atom }); i >= 0 {
				gap.Missing, gap.Needs = res.Gaps[i].Missing, res.Gaps[i].Needs
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
	if len(l.Sites) == 0 || len(l.ObservedFamilies) == 0 {
		issues = append(issues, "the ledger judged no observed guard")
	}
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

// RequireObserved fails unless every named guard family and recipient has at
// least one observed evaluation, so a registered kind cannot pass unjudged.
func (l *ConditionLedger) RequireObserved(families, recipients []string) error {
	var missing []string
	for _, f := range families {
		if l.ObservedFamilies[f] == 0 {
			missing = append(missing, "family "+f)
		}
	}
	for _, r := range recipients {
		if l.ObservedRecipients[r] == 0 {
			missing = append(missing, "recipient "+r)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("no observed guard for %s", strings.Join(missing, ", "))
	}
	return nil
}
