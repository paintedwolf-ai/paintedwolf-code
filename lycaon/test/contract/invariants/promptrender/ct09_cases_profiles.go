package promptrender

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/test/contract/internal/casegen"
	"github.com/lycaon/lycaon/test/contract/internal/hoststate"
	"github.com/lycaon/lycaon/test/contract/internal/independence"
	"github.com/lycaon/lycaon/test/contract/internal/interactions"
)

// profileCaseBudget bounds the generated profile cases: runs per gap and in
// total.
var profileCaseBudget = casegen.Budget{PerTarget: 60, Total: 8000}

// profileCaseProducer renders standing-family guards under a customized tool
// profile. A device pack may own tools/profiles/coordinator and a project agent
// may run any tool profile (docs/extend.md), so each tool a guard names may be
// loaded, deferred or absent. The reader comes from prompts.NewReader over the
// coordinator's surface plan or a worker profile, as the skill cases build it;
// the coordinator renders its standing prompt with a project phase that binds
// the guard's mode or surface template, the worker renders the implementer
// persona.
type profileCaseProducer struct {
	s        *Stock
	readers  *skillCaseProducer
	base     *hoststate.CoverageTracker
	sites    map[string]interactions.ConditionSite
	rendered map[string][]hoststate.ObservedCondition
	observed map[string]*hoststate.ObservedCondition
	runs     int
}

func newProfileCaseProducer(s *Stock, sites []interactions.ConditionSite) (*profileCaseProducer, error) {
	base, err := NewConditionTracker(s)
	if err != nil {
		return nil, err
	}
	p := &profileCaseProducer{
		s: s, readers: &skillCaseProducer{stock: s}, base: base, sites: map[string]interactions.ConditionSite{},
		rendered: map[string][]hoststate.ObservedCondition{}, observed: map[string]*hoststate.ObservedCondition{},
	}
	for _, site := range sites {
		if site.Family == "template" && inStandingFamily(site.ID) {
			p.sites[site.ID] = site
		}
	}
	return p, nil
}

func (p *profileCaseProducer) siteFacts(site string) []string {
	var facts []string
	for _, atom := range p.sites[site].Atoms {
		for _, f := range atomRefs(atom) {
			if !slices.Contains(facts, f) {
				facts = append(facts, f)
			}
		}
	}
	return facts
}

// Knobs lists one knob per tool site's facts name, the command effects when a
// command fact is read, and the root count when a root fact is read.
func (p *profileCaseProducer) Knobs(site, recipient string) []casegen.Knob {
	if _, ok := p.sites[site]; !ok {
		return nil
	}
	if recipient != interactions.RecipientCoordinator && recipient != interactions.RecipientWorker {
		return nil
	}
	knobs := readerKnobs(p.siteFacts(site))
	if !slices.ContainsFunc(knobs, func(k casegen.Knob) bool { return strings.HasPrefix(k.Name, "tool:") }) {
		return nil
	}
	return knobs
}

// Run renders the recipient's standing text for the case once and returns the
// evaluations of site.
func (p *profileCaseProducer) Run(ctx context.Context, site, recipient, id string, knobs []casegen.Knob, s casegen.Setting) ([]casegen.Vector, error) {
	key := recipient + "|" + standingTemplates(site) + "|" + casegen.Label(knobs, s)
	conds, done := p.rendered[key]
	if !done {
		var err error
		if conds, err = p.render(ctx, site, recipient, recipient+":profile"+casegen.Label(knobs, s), knobs, s); err != nil {
			return nil, err
		}
		p.rendered[key] = conds
	}
	var out []casegen.Vector
	for _, c := range conds {
		if c.Site != site {
			continue
		}
		for _, v := range c.Vectors {
			if v.Partition != recipient || !v.Recomposes {
				continue
			}
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
	return out, nil
}

// standingTemplates names the mode or surface template a coordinator case
// binds for site; other sites render under the investigate surface's own.
func standingTemplates(site string) string {
	name := strings.TrimPrefix(site, "agents/prompts/")
	switch {
	case strings.HasPrefix(name, "coordinator-mode-"):
		return "mode:" + strings.TrimPrefix(name, "coordinator-mode-")
	case strings.HasPrefix(name, "coordinator-surface-"):
		return "surface:agents/" + name + ".md"
	}
	return ""
}

func (p *profileCaseProducer) render(ctx context.Context, site, recipient, id string, knobs []casegen.Knob, s casegen.Setting) ([]hoststate.ObservedCondition, error) {
	var extra []string
	for _, f := range p.siteFacts(site) {
		extra = append(extra, factTools(f)...)
	}
	c := caseOf(knobs, s)
	if recipient == interactions.RecipientCoordinator {
		// A coordinator profile cannot defer a tool its surface lists on the
		// floor; only the surface's loadable tools load on request.
		floor, err := prompts.CompileSurfacePlan(skillCaseSurface, c.roots)
		if err != nil {
			return nil, err
		}
		for tool, v := range c.tools {
			if v == toolDeferred && floor.Immediate(tool) {
				return nil, nil
			}
		}
	}
	reader, ok, err := p.readers.reader(recipient, extra, c)
	if err != nil || !ok {
		return nil, err
	}
	skillsList, err := StockSkills(p.s)
	if err != nil {
		return nil, err
	}
	fork := p.base.Fork()
	fork.SetCase(id)
	engine := p.s.Engine()
	p.runs++
	restore := fork.Install()
	if recipient == interactions.RecipientCoordinator {
		err = p.renderCoordinator(ctx, engine, site, reader, c.roots, skillsList)
	} else if _, rErr := prompts.RenderPersona(ctx, engine, "implementer", reader, workerPersonaOccurrence(reader, skillsList)); rErr != nil {
		// A persona render the contract refuses is a spawn production refuses.
		restore()
		return nil, nil
	}
	restore()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", id, err)
	}
	conds := fork.Conditions()
	for _, cond := range conds {
		if cond.Family == "template" {
			p.merge(cond)
		}
	}
	return conds, nil
}

func (p *profileCaseProducer) renderCoordinator(ctx context.Context, engine *prompts.FileTemplateEngine, site string, reader prompts.Reader, roots int, skillsList []skills.Skill) error {
	tp := surface.TurnProfile{SurfaceID: skillCaseSurface, SurfaceTemplate: "agents/coordinator-surface-investigate.md", ModeRefs: []string{"implement-investigate"}}
	switch t := standingTemplates(site); {
	case strings.HasPrefix(t, "mode:"):
		tp.ModeRefs = []string{strings.TrimPrefix(t, "mode:")}
	case strings.HasPrefix(t, "surface:"):
		tp.SurfaceTemplate = strings.TrimPrefix(t, "surface:")
	}
	st := hoststate.CoordinatorState{TurnProfile: tp, RootCount: roots, SpawnAllowlist: spawn.AmbientAllowedAgents()}
	for i := range roots {
		st.Roots = append(st.Roots, hoststate.RootSpec{Ref: projectroot.RootRef{ID: fmt.Sprintf("r%d", i+1), Label: fmt.Sprintf("root%d", i+1), Path: fmt.Sprintf("/tmp/fixture-root-%d", i+1), IsPrimary: i == 0}})
	}
	vars, err := buildCoordinatorStandingVars(st, reader, skillsList, prompts.ExecutionModePromptTransition{}, true)
	if err != nil {
		return err
	}
	unitCat, err := prompts.UnitCatalogForEngine(engine)
	if err != nil {
		return err
	}
	_, err = renderStandingFromVars(ctx, engine, unitCat, tp, reader.Selection(), vars)
	return err
}

func (p *profileCaseProducer) merge(c hoststate.ObservedCondition) {
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

// Observations returns every template guard evaluation the profile cases
// recorded.
func (p *profileCaseProducer) Observations() []hoststate.ObservedCondition {
	out := make([]hoststate.ObservedCondition, 0, len(p.observed))
	for _, id := range slices.Sorted(maps.Keys(p.observed)) {
		out = append(out, *p.observed[id])
	}
	return out
}

// targets turns each standing-family gap on a coordinator or worker whose
// atoms name a tool into a generation target.
func (p *profileCaseProducer) targets(l *interactions.ConditionLedger) ([]casegen.Target, []string) {
	var out []casegen.Target
	var gapRecipients []string
	for _, gap := range l.Gaps {
		if _, ok := p.sites[gap.Site]; !ok || len(gap.Needs) == 0 {
			continue
		}
		recipients := []string{gap.Recipient}
		if gap.Recipient == interactions.RecipientAny {
			recipients = []string{interactions.RecipientCoordinator, interactions.RecipientWorker}
		}
		for _, r := range recipients {
			if len(p.Knobs(gap.Site, r)) == 0 {
				continue
			}
			out = append(out, casegen.Target{Site: gap.Site, Recipient: r, Input: gap.Atom, Needs: gap.Needs, References: atomRefs})
			gapRecipients = append(gapRecipients, gap.Recipient)
		}
	}
	return out, gapRecipients
}
