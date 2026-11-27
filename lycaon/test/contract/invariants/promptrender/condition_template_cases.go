package promptrender

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/pongoplain"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/contract/internal/casegen"
	"github.com/lycaon/lycaon/test/contract/internal/hoststate"
	"github.com/lycaon/lycaon/test/contract/internal/independence"
	"github.com/lycaon/lycaon/test/contract/internal/interactions"
)

// maxKickReaders caps the distinct recipient readers a template case draws
// from.
const maxKickReaders = 12

// kickCaseProducer renders generated kick occurrences: each case picks a
// kick whose template reaches the guard, a recipient reader, and values of
// the kick's declared envelope variables from their typed domains, and renders
// it through the production kick renderer with branch observation on.
type kickCaseProducer struct {
	s        *Stock
	reg      *anchor.Registry
	base     *hoststate.CoverageTracker
	coord    []coordRep
	workers  []workerRep
	kickUnit map[string]string
	vars     map[string][]hoststate.AnchorVarSpec
	reaches  map[string][]string
	sources  map[string]string
	observed map[string]*hoststate.ObservedCondition
	runs     int
}

func newKickCaseProducer(s *Stock, sites []interactions.ConditionSite) (*kickCaseProducer, error) {
	reg, err := anchor.LoadRegistryFromConfigRoot()
	if err != nil {
		return nil, err
	}
	base, err := NewConditionTracker(s)
	if err != nil {
		return nil, err
	}
	cReps, err := cachedCoordinatorReps()
	if err != nil {
		return nil, err
	}
	wReps, err := cachedWorkerReps()
	if err != nil {
		return nil, err
	}
	templates, err := interactions.TemplateUnits(s.Catalog)
	if err != nil {
		return nil, err
	}
	specs, err := hoststate.AnchorCatalogSpecs()
	if err != nil {
		return nil, err
	}
	p := &kickCaseProducer{
		s: s, reg: reg, base: base,
		coord:    distinctReps(cReps, func(r coordRep) string { return r.auth.Reader.Fingerprint() }),
		workers:  distinctReps(wReps, func(r workerRep) string { return r.auth.Reader.Fingerprint() }),
		kickUnit: map[string]string{}, vars: map[string][]hoststate.AnchorVarSpec{}, reaches: map[string][]string{}, sources: map[string]string{},
		observed: map[string]*hoststate.ObservedCondition{},
	}
	for _, kick := range slices.Concat(informKickIDs(reg, "coordinator-", "session-agent-enabled"), informKickIDs(reg, "worker-")) {
		unit, ok := interactions.TemplateIdentity(s.Catalog, templates, prompts.KicksTemplatePrefix+kick+".md")
		if !ok {
			continue
		}
		p.kickUnit[kick] = unit
		if aID, ok := reg.LookupRender(kick); ok {
			p.vars[kick] = specs[string(aID)].Vars
		}
	}
	for _, site := range sites {
		p.sources[site.ID] = site.Source
	}
	p.indexReach(sites)
	return p, nil
}

func distinctReps[R any](reps []R, fingerprint func(R) string) []R {
	var out []R
	seen := map[string]bool{}
	for _, r := range reps {
		if fp := fingerprint(r); !seen[fp] && len(out) < maxKickReaders {
			seen[fp] = true
			out = append(out, r)
		}
	}
	return out
}

// indexReach maps each template unit to the kicks whose template includes it,
// directly or through other includes.
func (p *kickCaseProducer) indexReach(sites []interactions.ConditionSite) {
	includedBy := map[string][]string{}
	for _, s := range sites {
		for _, inc := range s.Includers {
			unit, _, _ := strings.Cut(inc, ":L")
			if !slices.Contains(includedBy[s.ContributionID], unit) {
				includedBy[s.ContributionID] = append(includedBy[s.ContributionID], unit)
			}
		}
	}
	byUnit := map[string][]string{}
	for kick, unit := range p.kickUnit {
		byUnit[unit] = append(byUnit[unit], kick)
	}
	for _, s := range sites {
		unit := s.ContributionID
		if _, done := p.reaches[unit]; done {
			continue
		}
		seen := map[string]bool{unit: true}
		queue := []string{unit}
		var kicks []string
		for len(queue) > 0 {
			u := queue[0]
			queue = queue[1:]
			kicks = append(kicks, byUnit[u]...)
			for _, up := range includedBy[u] {
				if !seen[up] {
					seen[up] = true
					queue = append(queue, up)
				}
			}
		}
		slices.Sort(kicks)
		p.reaches[unit] = slices.Compact(kicks)
	}
}

func siteUnit(site string) string {
	unit, _, _ := strings.Cut(site, "@")
	return unit
}

func (p *kickCaseProducer) kicksFor(site, recipient string) []string {
	var out []string
	for _, k := range p.reaches[siteUnit(site)] {
		if strings.HasPrefix(k, "worker-") == (recipient == interactions.RecipientWorker) {
			out = append(out, k)
		}
	}
	return out
}

// templateReferences names the top-level variables a template atom reads.
func templateReferences(atom string) []string {
	expr, err := pongoplain.ParseExpression(atom)
	if err != nil {
		return nil
	}
	var out []string
	for _, id := range expr.Identifiers() {
		top, _, _ := strings.Cut(id, ".")
		if !slices.Contains(out, top) {
			out = append(out, top)
		}
	}
	return out
}

// Knobs lists the kick, the recipient reader and each envelope variable of a
// reaching kick that the guard reads.
func (p *kickCaseProducer) Knobs(site, recipient string) []casegen.Knob {
	kicks := p.kicksFor(site, recipient)
	readers := len(p.coord)
	if recipient == interactions.RecipientWorker {
		readers = len(p.workers)
	}
	if len(kicks) == 0 || readers == 0 {
		return nil
	}
	knobs := []casegen.Knob{{Name: "kick", Facts: []string{casegen.AnyFact}}, {Name: "reader", Facts: []string{casegen.AnyFact}}}
	for _, k := range kicks {
		knobs[0].Values = append(knobs[0].Values, k)
	}
	for i := range readers {
		knobs[1].Values = append(knobs[1].Values, i)
	}
	read := map[string]bool{}
	for _, f := range templateReferences(p.sources[site]) {
		read[f] = true
	}
	seen := map[string]bool{}
	for _, k := range kicks {
		for _, v := range p.vars[k] {
			if !read[v.Name] || seen[v.Name] {
				continue
			}
			values, err := hoststate.AnchorVarDomain(v)
			if err != nil {
				continue
			}
			seen[v.Name] = true
			knobs = append(knobs, casegen.Knob{Name: "var." + v.Name, Facts: []string{v.Name}, Values: append([]any{absentVar{}}, values...)})
		}
	}
	return knobs
}

// absentVar leaves an envelope variable to the kick's base occurrence data.
type absentVar struct{}

func (absentVar) String() string { return "base" }

func isAbsent(v any) bool {
	_, ok := v.(absentVar)
	return ok
}

// Run renders one generated kick occurrence for the case's recipient.
func (p *kickCaseProducer) Run(ctx context.Context, site, recipient, id string, knobs []casegen.Knob, s casegen.Setting) ([]casegen.Vector, error) {
	kick := s.Value(knobs[0]).(string)
	readerIdx := s.Value(knobs[1]).(int)
	data := maps.Clone(kickEnvelopeVariants(p.reg, kick)[0])
	for _, k := range knobs[2:] {
		name := strings.TrimPrefix(k.Name, "var.")
		if v := s.Value(k); !isAbsent(v) {
			data[name] = v
		}
	}
	reader, offered, ok, err := p.recipientReader(kick, recipient, readerIdx, data)
	if err != nil || !ok {
		return nil, err
	}
	if !kickOccurrenceProducible(kick, data, reader, offered) {
		return nil, nil
	}
	p.runs++
	fork := p.base.Fork()
	fork.SetCase(id)
	restore := fork.Install()
	_, err = p.s.Engine().RenderKick(ctx, reader, kick, data)
	restore()
	if err != nil {
		return nil, fmt.Errorf("render kick %s: %w", kick, err)
	}
	var out []casegen.Vector
	for _, c := range fork.Conditions() {
		if c.Family != "template" {
			continue
		}
		p.merge(c)
		if c.Site != site {
			continue
		}
		for _, v := range c.Vectors {
			if v.Partition == recipient && v.Recomposes {
				vec := casegen.Vector{Truths: map[string]independence.Truth{}, Decision: v.Decision, Evaluated: true, Group: strings.Join(v.Path, ">")}
				for i, a := range c.Atoms {
					if prev, seen := vec.Truths[a]; seen && prev != v.Atoms[i] {
						vec.Truths[a] = independence.Unknown
						continue
					}
					vec.Truths[a] = v.Atoms[i]
				}
				out = append(out, vec)
			}
		}
	}
	return out, nil
}

// recipientReader is the reader the kick renders for, as the kick sweep
// picks it: closeout kicks render for the closeout turn's reader.
func (p *kickCaseProducer) recipientReader(kick, recipient string, idx int, data map[string]any) (prompts.Reader, []string, bool, error) {
	if recipient == interactions.RecipientWorker {
		wr := p.workers[idx]
		if kick == "worker-closeout" || kick == "worker-cancel-closeout" {
			r, err := workerCloseoutReader(wr.ws)
			return r, []string{"complete_leg"}, err == nil, err
		}
		return wr.auth.Reader, wr.auth.Offered, true, nil
	}
	cr := p.coord[idx]
	reader := cr.auth.Reader
	if kick == "coordinator-turn-closeout" || kick == "session-agent-enabled" {
		r, err := closeoutTurnReader(cr.state, false)
		if err != nil {
			return prompts.Reader{}, nil, false, err
		}
		reader = r
		data["session_command_mutation_capable"] = cr.state.SessionMode != api.SessionModeAsk && cr.auth.Reader.Held()["command_mutation"]
	}
	if err := prompts.MergeCoordinatorKickVarsForTools(data, reader); err != nil {
		return prompts.Reader{}, nil, false, err
	}
	return reader, cr.auth.Offered, true, nil
}

func (p *kickCaseProducer) merge(c hoststate.ObservedCondition) {
	cur := p.observed[c.Site]
	if cur == nil {
		cp := c
		cp.Vectors = nil
		p.observed[c.Site], cur = &cp, &cp
	}
	for _, v := range c.Vectors {
		if !slices.ContainsFunc(cur.Vectors, func(w hoststate.ObservedVector) bool {
			return w.Decision == v.Decision && w.Partition == v.Partition && slices.Equal(w.Atoms, v.Atoms) && slices.Equal(w.Path, v.Path)
		}) {
			cur.Vectors = append(cur.Vectors, v)
		}
	}
}

// Observations returns every template guard evaluation the generated kicks
// produced.
func (p *kickCaseProducer) Observations() []hoststate.ObservedCondition {
	out := make([]hoststate.ObservedCondition, 0, len(p.observed))
	for _, id := range slices.Sorted(maps.Keys(p.observed)) {
		out = append(out, *p.observed[id])
	}
	return out
}
