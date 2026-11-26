package promptrender

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/pongoplain"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/contract/internal/casegen"
	"github.com/lycaon/lycaon/test/contract/internal/hoststate"
	"github.com/lycaon/lycaon/test/contract/internal/independence"
	"github.com/lycaon/lycaon/test/contract/internal/interactions"
)

// standingCaseBudget bounds the generated standing cases: runs per gap and in
// total. Runs of one recipient's setting are rendered once and shared by
// every site.
var standingCaseBudget = casegen.Budget{PerTarget: 30, Total: 4000}

// standingConstraints are the production invariants that couple standing
// reader facts to the root count.
var standingConstraints = []interactions.AtomConstraint{
	{Atom: "runner_write_root_tools", Implies: "has_workspace_roots", Recipient: interactions.RecipientCoordinator, Source: srcCoordinatorNoFolder},
	{Atom: "runner_write_root_tools", Implies: "has_workspace_roots", Recipient: interactions.RecipientWorker, Source: srcWorkerWorkspace},
	{Atom: "profile_has_write_tools", Implies: "has_workspace_roots", Recipient: interactions.RecipientWorker, Source: srcWorkerWorkspace},
}

const (
	srcCoordinatorNoFolder = "internal/prompts/reader.go:CompileSurfacePlan keeps only coordinator-surfaces.yaml no_folder_allowlist at zero roots, " +
		"which omits command; runner_write_root_tools needs command with mutation and external exec (reader_facts.go)"
	srcWorkerWorkspace = "internal/worker/task_tool.go refuses a zero-root spawn (WORKER_WORKSPACE_REQUIRED) unless prompts.AgentRunsWithoutWorkspace, " +
		"which is false for a mutation-capable profile (AgentMutationCapable) and for one reaching command (ProfileSurveysProjectTree)"
)

// standingFamilies are the template units whose gaps the standing producer
// searches: shared partials, units and archetypes, and agent prompts.
var standingFamilies = []string{"shared/", "agents/"}

// standingCaseProducer renders generated host states through the production
// seed resolvers: each case picks coordinator or worker seed coordinates from
// the seed domains, resolves and freezes the state, and renders the standing
// prompt or persona and the tool-procedure block with branch observation on.
type standingCaseProducer struct {
	s        *Stock
	domains  *hoststate.SeedDomains
	base     *hoststate.CoverageTracker
	coord    []casegen.Knob
	worker   []casegen.Knob
	rendered map[string][]hoststate.ObservedCondition
	observed map[string]*hoststate.ObservedCondition
	runs     int
}

func newStandingCaseProducer(s *Stock) (*standingCaseProducer, error) {
	domains, err := hoststate.DefaultSeedDomains()
	if err != nil {
		return nil, err
	}
	base, err := NewConditionTracker(s)
	if err != nil {
		return nil, err
	}
	p := &standingCaseProducer{
		s: s, domains: domains, base: base,
		rendered: map[string][]hoststate.ObservedCondition{}, observed: map[string]*hoststate.ObservedCondition{},
	}
	p.coord = coordinatorSeedKnobs(domains)
	p.worker = workerSeedKnobs(domains)
	return p, nil
}

// leading returns values with first moved to the front, so the first value is
// the baseline every case starts from.
func leading[T comparable](values []T, first T) []any {
	out := []any{first}
	for _, v := range values {
		if v != first {
			out = append(out, v)
		}
	}
	return out
}

var (
	rootFacts = []string{"has_workspace_roots", "multi_root", "root_count", "has_git", "has_any_git_root", "has_file_tools"}
	webFacts  = []string{"web_search_enabled", "profile_has_web_search", "profile_has_fetch_url", "can_spawn_web_research"}
	tempFacts = []string{"session_scratch_dir", "shared_temp_writable", "shared_temp_locations"}
	extFacts  = []string{"command_external_hidden", "command_external_shown", "can_command_external_selector"}
	any1      = []string{casegen.AnyFact}
	bools     = []any{false, true}
)

func coordinatorSeedKnobs(d *hoststate.SeedDomains) []casegen.Knob {
	surfaces := []any{""}
	for _, s := range d.Surfaces {
		surfaces = append(surfaces, s.ID)
	}
	var postures []api.SessionPosture
	for _, p := range d.Postures {
		postures = append(postures, p.ID)
	}
	overlays := make([]any, len(d.Overlays))
	for i := range d.Overlays {
		overlays[i] = i
	}
	return []casegen.Knob{
		{Name: "surface", Facts: any1, Values: surfaces},
		{Name: "posture", Facts: any1, Values: leading(postures, api.SessionPostureBuild)},
		{Name: "mode", Facts: any1, Values: leading(d.SessionModes, api.SessionModeAgent)},
		{Name: "loading", Facts: any1, Values: leading(d.LoadingModes, hoststate.LoadingAllLoaded)},
		{Name: "workflow", Facts: any1, Values: leading(d.WorkflowStates, hoststate.WorkflowAmbientIdle)},
		{Name: "overlay", Facts: any1, Values: overlays},
		{Name: "roots", Facts: rootFacts, Values: leading(d.RootTopologies, hoststate.RootsSingleGit)},
		{Name: "vision", Facts: []string{"caps"}, Values: bools},
		{Name: "web", Facts: webFacts, Values: []any{true, false}},
		{Name: "verify", Facts: []string{"verify_command"}, Values: bools},
		{Name: "ext_hidden", Facts: extFacts, Values: bools},
		{Name: "approval", Facts: any1, Values: leading(d.ApprovalPostures, gate.PostureBalanced)},
		{Name: "temp", Facts: tempFacts, Values: leading(d.TempStates, hoststate.TempScratchOnly)},
		{Name: "mcp", Facts: any1, Values: leading(d.MCPStates, hoststate.MCPNone)},
	}
}

func workerSeedKnobs(d *hoststate.SeedDomains) []casegen.Knob {
	var agents []string
	for _, a := range d.Overlays[0].WorkerAgents {
		agents = append(agents, a.ID)
	}
	var postures []api.SessionPosture
	for _, p := range d.Postures {
		postures = append(postures, p.ID)
	}
	overlays := make([]any, len(d.Overlays))
	for i := range d.Overlays {
		overlays[i] = i
	}
	return []casegen.Knob{
		{Name: "agent", Facts: any1, Values: leading(agents, "implementer")},
		{Name: "posture", Facts: any1, Values: leading(postures, api.SessionPostureBuild)},
		{Name: "parent_mode", Facts: any1, Values: leading(d.SessionModes, api.SessionModeAgent)},
		{Name: "loading", Facts: any1, Values: leading(d.WorkerLoadings, hoststate.WorkerLoadingAllLoaded)},
		{Name: "branch", Facts: any1, Values: leading(d.WorkerBranches, hoststate.BranchMainGit)},
		{Name: "overlay", Facts: any1, Values: overlays},
		{Name: "roots", Facts: rootFacts, Values: []any{1, 0, 2}},
		{Name: "secondary_git", Facts: rootFacts, Values: bools},
		{Name: "upgraded", Facts: any1, Values: bools},
		{Name: "vision", Facts: []string{"caps"}, Values: bools},
		{Name: "web", Facts: webFacts, Values: []any{true, false}},
		{Name: "ext_hidden", Facts: extFacts, Values: bools},
		{Name: "approval", Facts: any1, Values: leading(d.ApprovalPostures, gate.PostureBalanced)},
		{Name: "temp", Facts: tempFacts, Values: leading(d.TempStates, hoststate.TempNone)},
	}
}

func inStandingFamily(site string) bool {
	return slices.ContainsFunc(standingFamilies, func(prefix string) bool { return strings.HasPrefix(site, prefix) })
}

// standingReferences names the top-level variables a template atom reads.
func standingReferences(atom string) []string {
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

// Knobs lists the seed coordinates of the recipient's standing render.
func (p *standingCaseProducer) Knobs(site, recipient string) []casegen.Knob {
	if !inStandingFamily(site) {
		return nil
	}
	switch recipient {
	case interactions.RecipientCoordinator:
		return p.coord
	case interactions.RecipientWorker:
		return p.worker
	}
	return nil
}

// Run renders the case's standing prompt once per recipient and setting and
// returns the evaluations of site on recipient.
func (p *standingCaseProducer) Run(ctx context.Context, site, recipient, id string, knobs []casegen.Knob, s casegen.Setting) ([]casegen.Vector, error) {
	key := recipient + casegen.Label(knobs, s)
	conds, done := p.rendered[key]
	if !done {
		var err error
		if conds, err = p.render(ctx, recipient, recipient+":generated"+casegen.Label(knobs, s), knobs, s); err != nil {
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
			vec := casegen.Vector{Truths: map[string]independence.Truth{}, Decision: v.Decision, Evaluated: true}
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

func (p *standingCaseProducer) render(ctx context.Context, recipient, id string, knobs []casegen.Knob, s casegen.Setting) ([]hoststate.ObservedCondition, error) {
	v := func(name string) any {
		for _, k := range knobs {
			if k.Name == name {
				return s.Value(k)
			}
		}
		panic("standing case knob " + name + " missing")
	}
	fork := p.base.Fork()
	fork.SetCase(id)
	var err error
	if recipient == interactions.RecipientCoordinator {
		err = p.renderCoordinator(ctx, fork, v)
	} else {
		err = p.renderWorker(ctx, fork, v)
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", id, err)
	}
	conds := fork.Conditions()
	for _, c := range conds {
		if c.Family == "template" {
			p.merge(c)
		}
	}
	return conds, nil
}

// renderCoordinator renders a resolved coordinator seed as the matrix does;
// a seed the production rules exclude renders nothing.
func (p *standingCaseProducer) renderCoordinator(ctx context.Context, fork *hoststate.CoverageTracker, v func(string) any) error {
	st, err := hoststate.ResolveCoordinatorSeed(p.domains, hoststate.CoordinatorSeed{
		OverlayIndex: v("overlay").(int), SessionMode: v("mode").(api.SessionMode), Posture: v("posture").(api.SessionPosture),
		WorkflowState: v("workflow").(hoststate.WorkflowState), SurfaceID: v("surface").(string),
		RootTopology: v("roots").(hoststate.RootTopology), Loading: v("loading").(hoststate.LoadingMode),
		WebSearch: v("web").(bool), Vision: v("vision").(bool), CommandExternalHidden: v("ext_hidden").(bool),
		ApprovalPosture: v("approval").(gate.Posture), TempState: v("temp").(hoststate.TempState), MCPState: v("mcp").(hoststate.MCPState),
	})
	if err != nil || st.Excluded {
		return err
	}
	auth, err := hoststate.FreezeTurnAuthority(ctx, st)
	if err != nil {
		return err
	}
	skillsList, err := overlaySkills(p.s, st.Overlay)
	if err != nil {
		return err
	}
	engine := engineForOverlay(p.s, st.Overlay)
	unitCat, err := prompts.UnitCatalogForEngine(engine)
	if err != nil {
		return err
	}
	vars, err := buildCoordinatorStandingVars(st, auth.Reader, skillsList, prompts.ExecutionModePromptTransition{}, v("verify").(bool))
	if err != nil {
		return err
	}
	p.runs++
	restore := fork.Install()
	defer restore()
	if _, err := renderStandingFromVars(ctx, engine, unitCat, st.TurnProfile, auth.Reader.Selection(), vars); err != nil {
		return err
	}
	_, err = inject.RenderToolProceduresBlock(ctx, prompts.NewInjectRenderer(engine), "sess-proc", auth.Reader)
	return err
}

// renderWorker renders a resolved worker seed's persona and tool procedures
// as the matrix does; a seed the production rules exclude renders nothing.
func (p *standingCaseProducer) renderWorker(ctx context.Context, fork *hoststate.CoverageTracker, v func(string) any) error {
	ws, err := hoststate.ResolveWorkerSeed(ctx, p.domains, hoststate.WorkerSeed{
		OverlayIndex: v("overlay").(int), AgentID: v("agent").(string), Posture: v("posture").(api.SessionPosture),
		BranchView: v("branch").(hoststate.WorkerBranchView), RootCount: v("roots").(int), SecondaryRootGit: v("secondary_git").(bool),
		Loading: v("loading").(hoststate.WorkerLoadingMode), ParentSessionMode: v("parent_mode").(api.SessionMode),
		ParentUpgradedAskToAgent: v("upgraded").(bool), WebSearch: v("web").(bool), Vision: v("vision").(bool),
		CommandExternalHidden: v("ext_hidden").(bool), ApprovalPosture: v("approval").(gate.Posture), TempState: v("temp").(hoststate.TempState),
	})
	if err != nil || ws.Excluded {
		return err
	}
	auth, err := hoststate.FreezeWorkerAuthority(ctx, ws)
	if err != nil {
		return err
	}
	skillsList, err := overlaySkills(p.s, ws.Overlay)
	if err != nil {
		return err
	}
	engine := engineForOverlay(p.s, ws.Overlay)
	p.runs++
	restore := fork.Install()
	defer restore()
	if _, err := prompts.RenderPersona(ctx, engine, ws.AgentID, auth.Reader, workerPersonaOccurrence(auth.Reader, skillsList)); err != nil {
		return err
	}
	_, err = inject.RenderToolProceduresBlock(ctx, prompts.NewInjectRenderer(engine), "sess-proc", auth.Reader)
	return err
}

func (p *standingCaseProducer) merge(c hoststate.ObservedCondition) {
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

// Observations returns every template guard evaluation the generated
// standing cases produced.
func (p *standingCaseProducer) Observations() []hoststate.ObservedCondition {
	out := make([]hoststate.ObservedCondition, 0, len(p.observed))
	for _, id := range slices.Sorted(maps.Keys(p.observed)) {
		out = append(out, *p.observed[id])
	}
	return out
}

// targets turns each gap of a standing-family guard into generation targets;
// a guard nobody evaluated is searched on both recipients.
func (p *standingCaseProducer) targets(l *interactions.ConditionLedger) ([]casegen.Target, []string) {
	byID := map[string]interactions.ConditionSite{}
	for _, s := range l.Sites {
		byID[s.ID] = s
	}
	var out []casegen.Target
	var gapRecipients []string
	for _, gap := range l.Gaps {
		site := byID[gap.Site]
		if site.Family != "template" || !inStandingFamily(gap.Site) {
			continue
		}
		needs := gap.Needs
		if len(needs) == 0 {
			if expr, err := pongoplain.ParseExpression(site.Source); err == nil {
				needs = casegen.Needs(distinctAtoms(site.Atoms), gap.Atom, expr.Decide, 16)
			}
		}
		recipients := []string{gap.Recipient}
		if gap.Recipient == interactions.RecipientAny {
			recipients = []string{interactions.RecipientCoordinator, interactions.RecipientWorker}
		}
		for _, r := range recipients {
			out = append(out, casegen.Target{Site: gap.Site, Recipient: r, Input: gap.Atom, Needs: needs, References: standingReferences})
			gapRecipients = append(gapRecipients, gap.Recipient)
		}
	}
	return out, gapRecipients
}

// generateStandingCases searches the standing-family gaps of first and
// returns the observations, outcomes and producer runs.
func generateStandingCases(ctx context.Context, s *Stock, first *interactions.ConditionLedger) ([]hoststate.ObservedCondition, []casegen.Outcome, []string, int, error) {
	p, err := newStandingCaseProducer(s)
	if err != nil {
		return nil, nil, nil, 0, err
	}
	targets, gapRecipients := p.targets(first)
	outcomes, err := casegen.Generate(ctx, p, targets, standingCaseBudget)
	if err != nil {
		return nil, nil, nil, 0, err
	}
	return p.Observations(), outcomes, gapRecipients, p.runs, nil
}
