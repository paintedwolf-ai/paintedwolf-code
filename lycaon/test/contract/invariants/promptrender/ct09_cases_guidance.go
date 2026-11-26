package promptrender

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/pongoplain"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/toolvocab/toolvocabtest"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/contract/internal/casegen"
	"github.com/lycaon/lycaon/test/contract/internal/hoststate"
	"github.com/lycaon/lycaon/test/contract/internal/independence"
	"github.com/lycaon/lycaon/test/contract/internal/interactions"
)

// guidanceCase is one guidance template a production producer delivers to
// one recipient, with the typed producer inputs that move its guards. Knob
// facts name the guard atoms each input drives.
type guidanceCase struct {
	unit      string
	recipient string
	knobs     []casegen.Knob
	render    func(ctx context.Context, e *guidanceCaseEnv, set casegen.Setting) (guidanceRender, error)
}

// guidanceRender is one production render and the authority its claims are
// checked against. An empty text is a setting the producer delivers nothing
// for.
type guidanceRender struct {
	text      string
	auth      hoststate.TurnAuthority
	mode      api.SessionMode
	posture   api.SessionPosture
	backticks bool
}

type guidanceCaseEnv struct {
	s     *Stock
	inj   *prompts.InjectRenderer
	gr    *prompts.GuidanceRenderer
	cReps []coordRep
	wReps []workerRep
	cases []guidanceCase
	now   time.Time
}

func newGuidanceCaseEnv(ctx context.Context, s *Stock) (*guidanceCaseEnv, error) {
	cReps, err := cachedCoordinatorReps()
	if err != nil {
		return nil, err
	}
	wReps, err := cachedWorkerReps()
	if err != nil {
		return nil, err
	}
	engine := s.Engine()
	e := &guidanceCaseEnv{
		s: s, inj: prompts.NewInjectRenderer(engine), gr: prompts.NewGuidanceRenderer(engine),
		cReps: cReps, wReps: wReps, now: time.Date(2026, 8, 1, 14, 0, 0, 0, time.UTC),
	}
	for _, build := range guidanceCaseBuilders {
		cs, err := build(ctx, e)
		if err != nil {
			return nil, err
		}
		e.cases = append(e.cases, cs...)
	}
	return e, nil
}

// guidanceCaseBuilders build each producer's cases from the stock recipients.
var guidanceCaseBuilders = []func(context.Context, *guidanceCaseEnv) ([]guidanceCase, error){
	viewContextCases,
	workerRoundCases,
	scanGuidanceCases,
	rejectCases,
	continuationRecordCases,
	sourceChangesCases,
	commandJobsCases,
	activeWorkflowCases,
	securityPhaseKickCases,
}

// knobValue returns the value set picks for the knob named name.
func knobValue[T any](knobs []casegen.Knob, set casegen.Setting, name string) T {
	for _, k := range knobs {
		if k.Name == name {
			v, _ := set.Value(k).(T)
			return v
		}
	}
	var zero T
	return zero
}

// indexKnob is a knob whose values index labels.
func indexKnob(name string, facts []string, labels []string) casegen.Knob {
	k := casegen.Knob{Name: name, Facts: facts, Labels: labels}
	for i := range labels {
		k.Values = append(k.Values, i)
	}
	return k
}

// oneAtATime is the baseline setting and every setting that moves one knob
// off it.
func oneAtATime(knobs []casegen.Knob) []casegen.Setting {
	out := []casegen.Setting{{}}
	for _, k := range knobs {
		for i := 1; i < len(k.Values); i++ {
			out = append(out, casegen.Setting{k.Name: i})
		}
	}
	return out
}

// runGuidanceCaseSweep renders every guidance case at its baseline and with
// each producer input moved alone, and checks each render's claims against
// the recipient's authority.
func runGuidanceCaseSweep(ctx context.Context, s *Stock, sum *CartesianSummary) error {
	e, err := newGuidanceCaseEnv(ctx, s)
	if err != nil {
		return err
	}
	for _, c := range e.cases {
		for _, set := range oneAtATime(c.knobs) {
			id := "guidance case " + c.recipient + ":" + c.unit + casegen.Label(c.knobs, set)
			sum.markCase(id)
			r, err := c.render(ctx, e, set)
			if err != nil {
				return fmt.Errorf("%s: %w", id, err)
			}
			if strings.TrimSpace(r.text) == "" {
				continue
			}
			sum.InjectAndGuidanceCount++
			sum.Violations = append(sum.Violations, checkClaimsOnce(s, id, r.text, r.auth, r.mode, r.posture, toolvocabtest.SubjectSelfNow(), nil, nil, nil, r.backticks)...)
		}
	}
	return nil
}

// guidanceCaseProducer runs generated guidance cases through their
// production producers and keeps every guard evaluation the template
// evaluator recorded.
type guidanceCaseProducer struct {
	e        *guidanceCaseEnv
	sources  map[string]string
	resolve  func(string) (string, bool)
	observed []hoststate.ObservedCondition
	runs     int
}

func newGuidanceCaseProducer(ctx context.Context, s *Stock) (*guidanceCaseProducer, error) {
	e, err := newGuidanceCaseEnv(ctx, s)
	if err != nil {
		return nil, err
	}
	sources, err := interactions.TemplateUnits(s.Catalog)
	if err != nil {
		return nil, err
	}
	for _, c := range e.cases {
		if _, ok := sources[c.unit]; !ok {
			return nil, fmt.Errorf("guidance case template %s is not a stock template unit", c.unit)
		}
	}
	return &guidanceCaseProducer{
		e: e, sources: sources,
		resolve: func(ref string) (string, bool) { return interactions.TemplateIdentity(s.Catalog, sources, ref) },
	}, nil
}

func siteUnit(site string) string {
	unit, _, _ := strings.Cut(site, "@")
	return unit
}

func (p *guidanceCaseProducer) caseFor(site, recipient string) *guidanceCase {
	for i := range p.e.cases {
		c := &p.e.cases[i]
		if c.unit == siteUnit(site) && c.recipient == recipient {
			return c
		}
	}
	return nil
}

// Knobs lists the producer inputs of the case delivering site to recipient.
func (p *guidanceCaseProducer) Knobs(site, recipient string) []casegen.Knob {
	if c := p.caseFor(site, recipient); c != nil {
		return c.knobs
	}
	return nil
}

// Run renders the case under a tracker that observes only the case's
// template and returns every evaluation of site on recipient.
func (p *guidanceCaseProducer) Run(ctx context.Context, site, recipient, id string, _ []casegen.Knob, set casegen.Setting) ([]casegen.Vector, error) {
	c := p.caseFor(site, recipient)
	if c == nil {
		return nil, fmt.Errorf("no guidance case delivers %s to %s", site, recipient)
	}
	tracker := hoststate.NewCoverageTracker()
	if err := tracker.RegisterTemplate(c.unit, p.sources[c.unit], pongoplain.Composed); err != nil {
		return nil, err
	}
	tracker.AttributeTemplateRefs(p.resolve)
	tracker.ObserveConditions(renderRecipient, occurrenceRecipient)
	tracker.SetCase(id)
	restore := tracker.Install()
	_, err := c.render(ctx, p.e, set)
	restore()
	p.runs++
	if err != nil {
		return nil, err
	}
	conds := tracker.Conditions()
	p.observed = interactions.MergeObservations(p.observed, conds)
	var out []casegen.Vector
	for _, oc := range conds {
		if oc.Site != site {
			continue
		}
		for _, v := range oc.Vectors {
			if v.Partition != recipient || !v.Recomposes {
				continue
			}
			truths := make(map[string]independence.Truth, len(oc.Atoms))
			for i, a := range oc.Atoms {
				truths[a] = v.Atoms[i]
			}
			out = append(out, casegen.Vector{Truths: truths, Decision: v.Decision, Evaluated: true})
		}
	}
	return out, nil
}

// guidanceGeneration is what targeted guidance generation added to a ledger.
type guidanceGeneration struct {
	targets, found, runs int
}

// generateGuidanceCases searches the guidance gaps of first with the guidance
// producers and joins what they observed, and why a search failed, into in.
func generateGuidanceCases(ctx context.Context, s *Stock, first *interactions.ConditionLedger, in *interactions.LedgerInput) (guidanceGeneration, error) {
	p, err := newGuidanceCaseProducer(ctx, s)
	if err != nil {
		return guidanceGeneration{}, err
	}
	byID := map[string]interactions.ConditionSite{}
	for _, site := range first.Sites {
		byID[site.ID] = site
	}
	var targets []casegen.Target
	var gapRecipients []string
	for _, gap := range first.Gaps {
		site := byID[gap.Site]
		if site.Family == "oar" {
			continue
		}
		recipients := []string{gap.Recipient}
		if gap.Recipient == interactions.RecipientAny {
			recipients = []string{interactions.RecipientCoordinator, interactions.RecipientWorker, unscopedRecipient}
		}
		for _, r := range recipients {
			if p.caseFor(gap.Site, r) == nil {
				continue
			}
			needs := gap.Needs
			if len(needs) == 0 {
				if expr, err := pongoplain.ParseExpression(site.Source); err == nil {
					needs = casegen.Needs(distinctAtoms(site.Atoms), gap.Atom, expr.Decide, 16)
				}
			}
			targets = append(targets, casegen.Target{
				Site: gap.Site, Recipient: r, Input: gap.Atom, Needs: needs,
				References: func(atom string) []string { return []string{atom} },
			})
			gapRecipients = append(gapRecipients, gap.Recipient)
		}
	}
	outcomes, err := casegen.Generate(ctx, p, targets, guidanceCaseBudget)
	if err != nil {
		return guidanceGeneration{}, err
	}
	in.Observed = interactions.MergeObservations(in.Observed, p.observed)
	if in.Searches == nil {
		in.Searches = map[string]string{}
	}
	maps.Copy(in.Searches, searchesOf(outcomes, gapRecipients))
	g := guidanceGeneration{targets: len(targets), runs: p.runs}
	for _, o := range outcomes {
		if o.Found {
			g.found++
		}
	}
	return g, nil
}

var guidanceCaseBudget = casegen.Budget{PerTarget: 60, Total: 2000}

func addGuidanceGeneration(sum *interactions.GenerationSummary, g guidanceGeneration) {
	sum.Targets += g.targets
	sum.Found += g.found
	sum.Runs += g.runs
}

// coordinatorRepsBy keeps the first stock coordinator recipient of each
// distinct key, in recipient order.
func coordinatorRepsBy(reps []coordRep, keep func(coordRep) bool, key func(coordRep) string) []coordRep {
	var out []coordRep
	seen := map[string]bool{}
	for _, cr := range reps {
		if cr.state.PackOverlayID != "stock" || !keep(cr) {
			continue
		}
		if k := key(cr); !seen[k] {
			seen[k] = true
			out = append(out, cr)
		}
	}
	return out
}

func offers(auth hoststate.TurnAuthority, tools ...string) string {
	var b strings.Builder
	for _, t := range tools {
		fmt.Fprintf(&b, "%s=%t;", t, slices.Contains(auth.Offered, t))
	}
	return b.String()
}

func readerFact(r prompts.Reader, name string) bool {
	v, _ := r.Facts()[name].(bool)
	return v
}

func repKnob(name string, facts []string, n int, label func(int) string) casegen.Knob {
	labels := make([]string, n)
	for i := range labels {
		labels[i] = label(i)
	}
	return indexKnob(name, facts, labels)
}

func coordinatorRender(text string, cr coordRep) guidanceRender {
	return guidanceRender{text: text, auth: cr.auth, mode: cr.state.SessionMode, posture: cr.state.Posture, backticks: true}
}

func workerRender(text string, wr workerRep) guidanceRender {
	return guidanceRender{text: text, auth: wr.auth, mode: wr.ws.ParentSessionMode, posture: wr.ws.Posture, backticks: true}
}

func (e *guidanceCaseEnv) zeroAuth() hoststate.TurnAuthority {
	return zeroToolAuthority(e.cReps[0].auth)
}

func (e *guidanceCaseEnv) unscopedRender(text string) guidanceRender {
	return guidanceRender{text: text, auth: e.zeroAuth()}
}

// anchorRegistry loads the stock anchor registry once per env.
func (e *guidanceCaseEnv) anchorRegistry() (*anchor.Registry, error) {
	return anchor.LoadRegistryFromConfigRoot()
}
