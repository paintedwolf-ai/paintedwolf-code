package promptrender

import (
	"context"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/pongoplain"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolsurface"
	"github.com/lycaon/lycaon/internal/toolvocab"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/contract/internal/casegen"
	"github.com/lycaon/lycaon/test/contract/internal/hoststate"
	"github.com/lycaon/lycaon/test/contract/internal/independence"
	"github.com/lycaon/lycaon/test/contract/internal/interactions"
)

// skillCaseBudget bounds targeted skill cases: runs per gap and in total.
var skillCaseBudget = casegen.Budget{PerTarget: 120, Total: 6000}

// skillCaseSurface is the coordinator surface generated coordinator cases
// read: it admits every command effect and offers or loads every tool the
// skill guards name that a coordinator surface offers.
const skillCaseSurface = "implement_investigate"

// skillCaseProfileID names the project tool profile a generated worker case
// runs under.
const skillCaseProfileID = "ct09-skill-case"

// Tool knob values: a tool loaded on the request, removed from the profile, or
// left deferred behind request_tools.
const (
	toolHeld = iota
	toolAbsent
	toolDeferred
)

// skillCaseProducer runs generated reader cases for skill guards. A case is
// the recipient's Reader from prompts.NewReader over typed producer inputs:
// the coordinator's surface plan with tools loaded, removed or deferred (a
// project pack may win tools/profiles/coordinator, docs/extend.md), or a
// worker's project tool profile (sandbox.ParseToolProfile admits any tool set
// and command_effects list); the command effect classes, root count, Git and
// the scratch folder. The skill renders only when
// toolvocab.SkillToolsOfferedForReader admits it, as discovery and
// skills_read do, through SkillVars with atom observation on.
type skillCaseProducer struct {
	stock    *Stock
	skills   map[string]skills.Skill
	sites    map[string]interactions.ConditionSite
	observed map[string]*hoststate.ObservedCondition
	runs     int
}

func newSkillCaseProducer(s *Stock, sites []interactions.ConditionSite) (*skillCaseProducer, error) {
	loaded, err := StockSkills(s)
	if err != nil {
		return nil, err
	}
	p := &skillCaseProducer{stock: s, skills: map[string]skills.Skill{}, sites: map[string]interactions.ConditionSite{}, observed: map[string]*hoststate.ObservedCondition{}}
	for _, sk := range loaded {
		if !sk.Project {
			p.skills[sk.Name] = sk
		}
	}
	for _, site := range sites {
		if site.Family == "skill" {
			p.sites[site.ID] = site
		}
	}
	return p, nil
}

func skillNameOf(site string) string {
	rest, ok := strings.CutPrefix(site, "skills/")
	if !ok {
		return ""
	}
	name, _, _ := strings.Cut(rest, "/")
	return name
}

var exprIdent = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)*`)

var exprKeywords = map[string]bool{"and": true, "or": true, "not": true, "in": true, "true": true, "false": true, "none": true, "True": true, "False": true, "None": true}

// atomRefs lists the reader facts a guard atom reads.
func atomRefs(atom string) []string {
	var out []string
	for _, id := range exprIdent.FindAllString(atom, -1) {
		if !exprKeywords[id] && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// commandFacts are reader facts that commandEffectFlags derives from the
// offered command and the admitted command effects.
var commandFacts = []string{
	"profile_command_mutation", "profile_command_external_exec", "profile_command_network_egress",
	"can_command_verify", "can_request_command_verify", "can_command_external_selector",
	"profile_has_terminal_capture", "can_git_read", "can_request_git_read", "can_git_commit",
}

// factTools names the tools whose presence moves fact.
func factTools(fact string) []string {
	switch {
	case slices.Contains(commandFacts, fact):
		return []string{"command"}
	case strings.HasPrefix(fact, "profile_has_"):
		if name := strings.TrimPrefix(fact, "profile_has_"); isTool(name) {
			return []string{name}
		}
	case strings.HasPrefix(fact, "profile_can_request."):
		if name := strings.TrimPrefix(fact, "profile_can_request."); isTool(name) {
			return []string{name}
		}
	}
	return nil
}

func isTool(name string) bool {
	_, ok := toolcontract.Lookup(name)
	return ok
}

// mintTools names the tools that mint evidence of kind.
func mintTools(kind string) []string {
	b := evidence.ActiveBinding()
	var out []string
	for _, tool := range b.DeclaredToolNames() {
		if b.ToolKind(tool) == kind && isTool(tool) {
			out = append(out, tool)
		}
	}
	slices.Sort(out)
	return out
}

// effectSets are the command effect sets a profile may declare, each with
// read_only; the full set comes first.
var effectSets = func() []toolcontract.CommandEffects {
	classes := []toolcontract.CommandEffectClass{toolcontract.CommandMutation, toolcontract.CommandExternalExec, toolcontract.CommandNetworkEgress}
	var out []toolcontract.CommandEffects
	for mask := 7; mask >= 0; mask-- {
		set := []toolcontract.CommandEffectClass{toolcontract.CommandReadOnly}
		for i, c := range classes {
			if mask&(1<<i) != 0 {
				set = append(set, c)
			}
		}
		e, err := toolcontract.NewCommandEffects(set...)
		if err != nil {
			panic(err)
		}
		out = append(out, e)
	}
	return out
}()

func (p *skillCaseProducer) siteFacts(site string) []string {
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

// Knobs lists the reader inputs that move site's facts: one per tool a fact
// names, one per evidence kind a mints fact names, the command effects, the
// root count, Git and the scratch folder.
func (p *skillCaseProducer) Knobs(site, recipient string) []casegen.Knob {
	if _, ok := p.skills[skillNameOf(site)]; !ok {
		return nil
	}
	if recipient != interactions.RecipientCoordinator && recipient != interactions.RecipientWorker {
		return nil
	}
	facts := p.siteFacts(site)
	var knobs []casegen.Knob
	var tools []string
	for _, f := range facts {
		for _, t := range factTools(f) {
			if !slices.Contains(tools, t) {
				tools = append(tools, t)
			}
		}
	}
	slices.Sort(tools)
	for _, t := range tools {
		drives := []string{"profile_has_" + t, "profile_can_request." + t}
		if t == "command" {
			drives = append(drives, commandFacts...)
		}
		for _, f := range facts {
			if kind, ok := strings.CutPrefix(f, "mints."); ok && slices.Contains(mintTools(kind), t) {
				drives = append(drives, f)
			}
		}
		knobs = append(knobs, casegen.Knob{Name: "tool:" + t, Facts: drives, Values: []any{toolHeld, toolAbsent, toolDeferred}, Labels: []string{"held", "absent", "deferred"}})
	}
	for _, f := range facts {
		if kind, ok := strings.CutPrefix(f, "mints."); ok {
			knobs = append(knobs, casegen.Knob{Name: "mint:" + kind, Facts: []string{f}, Values: []any{toolHeld, toolAbsent}, Labels: []string{"held", "absent"}})
		}
	}
	if slices.ContainsFunc(facts, func(f string) bool { return slices.Contains(commandFacts, f) }) {
		values := make([]any, len(effectSets))
		labels := make([]string, len(effectSets))
		for i, e := range effectSets {
			values[i] = e
			labels[i] = fmt.Sprint(e.Classes())
		}
		knobs = append(knobs, casegen.Knob{Name: "effects", Facts: commandFacts, Values: values, Labels: labels})
	}
	if slices.ContainsFunc(facts, func(f string) bool { return f == "root_count" || f == "has_workspace_roots" }) {
		knobs = append(knobs, casegen.Knob{Name: "roots", Facts: []string{"root_count", "has_workspace_roots", casegen.AnyFact}, Values: []any{1, 0, 2}})
	}
	if slices.Contains(facts, "session_scratch_dir") {
		knobs = append(knobs, casegen.Knob{Name: "scratch", Facts: []string{"session_scratch_dir"}, Values: []any{"/tmp/pw-scratch", ""}, Labels: []string{"set", "none"}})
	}
	if slices.ContainsFunc(facts, func(f string) bool { return f == "has_git" || strings.Contains(f, "git_") }) {
		knobs = append(knobs, casegen.Knob{Name: "git", Facts: []string{"has_git", "can_git_read", "can_request_git_read", "can_git_commit"}, Values: []any{true, false}})
	}
	return knobs
}

// skillCase is one generated case's producer inputs.
type skillCase struct {
	tools   map[string]int
	effects toolcontract.CommandEffects
	roots   int
	scratch string
	git     bool
}

func caseOf(knobs []casegen.Knob, s casegen.Setting) skillCase {
	c := skillCase{tools: map[string]int{}, effects: effectSets[0], roots: 1, scratch: "/tmp/pw-scratch", git: true}
	// A removed tool outranks a deferred one, which outranks a loaded one.
	rank := map[int]int{toolHeld: 0, toolDeferred: 1, toolAbsent: 2}
	set := func(tool string, v int) {
		if rank[v] > rank[c.tools[tool]] {
			c.tools[tool] = v
		}
	}
	for _, k := range knobs {
		v := s.Value(k)
		switch {
		case strings.HasPrefix(k.Name, "tool:"):
			set(strings.TrimPrefix(k.Name, "tool:"), v.(int))
		case strings.HasPrefix(k.Name, "mint:"):
			if v.(int) == toolAbsent {
				for _, t := range mintTools(strings.TrimPrefix(k.Name, "mint:")) {
					set(t, toolAbsent)
				}
			}
		case k.Name == "effects":
			c.effects = v.(toolcontract.CommandEffects)
		case k.Name == "roots":
			c.roots = v.(int)
		case k.Name == "scratch":
			c.scratch = v.(string)
		case k.Name == "git":
			c.git = v.(bool)
		}
	}
	return c
}

// reader builds recipient's Reader for c, or false when production admits no
// such session.
func (p *skillCaseProducer) reader(recipient string, extra []string, c skillCase) (prompts.Reader, bool, error) {
	profiles, err := hoststate.NarrowProfilesForPosture(p.stock.Profiles, api.SessionPostureBuild, false)
	if err != nil {
		return prompts.Reader{}, false, err
	}
	host := prompts.HostFacts{
		SessionMode: string(api.SessionModeAgent), Schemas: p.stock.Schemas, RootCount: c.roots,
		HasGit: c.git && c.roots > 0, HasAnyGitRoot: c.git && c.roots > 0, WebSearch: true, SessionScratchDir: c.scratch,
	}
	if recipient == interactions.RecipientCoordinator {
		// CompileSurfacePlan keeps only the no-folder allowlist at zero roots.
		plan, err := prompts.CompileSurfacePlan(skillCaseSurface, c.roots)
		if err != nil {
			return prompts.Reader{}, false, err
		}
		plan = plan.Promote(plan.DeferredNames()...)
		for _, name := range plan.AddressableNames() {
			// toolpolicy's listing drops worker-child tools from a coordinator.
			if !toolcontract.AdmitsSession(name, false) {
				plan = plan.Without(name)
			}
		}
		for tool, v := range c.tools {
			switch {
			case v == toolAbsent:
				plan = plan.Without(tool)
			case v == toolDeferred && plan.Addressable(tool):
				plan = plan.Defer(tool)
			}
		}
		for i := range profiles {
			if profiles[i].ID == prompts.CoordinatorProfileID {
				profiles[i].CommandEffects = c.effects
			}
		}
		host.Profiles, host.SpawnAllowlist = profiles, spawn.AmbientAllowedAgents()
		r, err := prompts.NewReader(host, prompts.TurnProjection{ProfileID: prompts.CoordinatorProfileID, SurfaceID: skillCaseSurface, Plan: plan, Offered: plan.ImmediateNames()})
		return r, err == nil, err
	}
	var base sandbox.ToolProfile
	for _, prof := range profiles {
		if prof.ID == "implement" {
			base = prof
		}
	}
	names := append(slices.Collect(maps.Keys(base.Tools)), extra...)
	profile := sandbox.ToolProfile{ID: skillCaseProfileID, Tools: map[string]bool{}, StickyTools: map[string]bool{}, DeferredTools: map[string]bool{}, WriteGlobs: base.WriteGlobs, ReadGlobs: base.ReadGlobs, CommandEffects: c.effects}
	var immediate, deferred []string
	for _, name := range names {
		// toolpolicy's listing drops addressed-session tools from a worker child.
		if strings.HasSuffix(name, "*") || profile.Tools[name] || !toolcontract.AdmitsSession(name, true) {
			continue
		}
		switch c.tools[name] {
		case toolAbsent:
			continue
		case toolDeferred:
			profile.DeferredTools[name] = true
			deferred = append(deferred, name)
		default:
			profile.StickyTools[name] = true
			immediate = append(immediate, name)
		}
		profile.Tools[name] = true
	}
	if len(deferred) > 0 && !profile.Tools["request_tools"] {
		profile.Tools["request_tools"], profile.StickyTools["request_tools"] = true, true
		immediate = append(immediate, "request_tools")
	}
	// worker/task_tool.go refuses a spawn without project roots unless
	// prompts.AgentRunsWithoutWorkspace: no mutation and no command, the
	// only tree-survey tool.
	if c.roots == 0 && (profile.Tools["command"] || prompts.ToolProfileMutationCapable(profile)) {
		return prompts.Reader{}, false, nil
	}
	plan := toolsurface.Compile(immediate, deferred)
	host.Profiles = append(profiles, profile)
	r, err := prompts.NewReader(host, prompts.TurnProjection{ProfileID: skillCaseProfileID, Plan: plan, Offered: plan.ImmediateNames()})
	return r, err == nil, err
}

// Run renders site's skill for one generated case and returns the recorded
// evaluations of site on recipient; a case production does not admit, or a
// reader the skill is not offered to, evaluates nothing.
func (p *skillCaseProducer) Run(ctx context.Context, site, recipient, id string, knobs []casegen.Knob, s casegen.Setting) ([]casegen.Vector, error) {
	name := skillNameOf(site)
	sk := p.skills[name]
	var extra []string
	for _, f := range p.siteFacts(site) {
		extra = append(extra, factTools(f)...)
		if kind, ok := strings.CutPrefix(f, "mints."); ok {
			extra = append(extra, mintTools(kind)...)
		}
	}
	p.runs++
	r, ok, err := p.reader(recipient, extra, caseOf(knobs, s))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", id, err)
	}
	if !ok || !toolvocab.SkillToolsOfferedForReader(sk, r) {
		return nil, nil
	}
	vars, err := prompts.SkillVars(r)
	if err != nil {
		return nil, err
	}
	tracker := hoststate.NewCoverageTracker()
	if err := tracker.RegisterTemplate("skills/"+name+"/SKILL.md", sk.Body, pongoplain.Isolated); err != nil {
		return nil, err
	}
	for _, rel := range sk.MarkdownResources() {
		raw, err := sk.ReadResource(rel)
		if err != nil {
			return nil, err
		}
		if err := tracker.RegisterTemplate("skills/"+name+"/"+rel, string(raw), pongoplain.Isolated); err != nil {
			return nil, err
		}
	}
	tracker.ObserveConditions(renderRecipient, occurrenceRecipient)
	tracker.SetCase(id)
	restore := tracker.Install()
	_, err = RenderSkill(ctx, sk, vars)
	restore()
	if err != nil {
		return nil, err
	}
	var out []casegen.Vector
	for _, c := range tracker.Conditions() {
		if !strings.HasPrefix(c.Site, "skills/"+name+"/") {
			continue
		}
		p.merge(c)
		if c.Site != site {
			continue
		}
		for _, v := range c.Vectors {
			if v.Partition == recipient && v.Recomposes {
				out = append(out, skillVector(c.Atoms, v))
			}
		}
	}
	return out, nil
}

func skillVector(atoms []string, v hoststate.ObservedVector) casegen.Vector {
	out := casegen.Vector{Truths: map[string]independence.Truth{}, Decision: v.Decision, Evaluated: true}
	for i, a := range atoms {
		if i >= len(v.Atoms) {
			break
		}
		if prev, ok := out.Truths[a]; ok && prev != v.Atoms[i] {
			out.Truths[a] = independence.Unknown
			continue
		}
		out.Truths[a] = v.Atoms[i]
	}
	return out
}

func (p *skillCaseProducer) merge(c hoststate.ObservedCondition) {
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

// Observations returns every skill guard evaluation the generated cases
// recorded.
func (p *skillCaseProducer) Observations() []hoststate.ObservedCondition {
	out := make([]hoststate.ObservedCondition, 0, len(p.observed))
	for _, id := range slices.Sorted(maps.Keys(p.observed)) {
		out = append(out, *p.observed[id])
	}
	return out
}

// Runs counts the generated cases.
func (p *skillCaseProducer) Runs() int { return p.runs }

// targets turns each skill gap on a coordinator or worker into a generation
// target.
func (p *skillCaseProducer) targets(l *interactions.ConditionLedger) ([]casegen.Target, []string) {
	var out []casegen.Target
	var gapRecipients []string
	for _, gap := range l.Gaps {
		if _, ok := p.sites[gap.Site]; !ok || len(gap.Needs) == 0 {
			continue
		}
		out = append(out, casegen.Target{Site: gap.Site, Recipient: gap.Recipient, Input: gap.Atom, Needs: gap.Needs, References: atomRefs})
		gapRecipients = append(gapRecipients, gap.Recipient)
	}
	return out, gapRecipients
}

// Production invariants over skill guard atoms.
const (
	srcCoordinatorNoFolder = "internal/coordinator/promptloop/worker_tool_plan.go:filterToolPlanForRootCount and prompts/reader.go:CompileSurfacePlan " +
		"keep only coordinator-surfaces.yaml no_folder_allowlist at zero roots, which omits command, command_output, render_view and skills_read"
	srcWorkerRoots = "internal/worker/task_tool.go refuses a zero-root spawn (WORKER_WORKSPACE_REQUIRED) unless prompts.AgentRunsWithoutWorkspace, " +
		"false for a profile reaching command (ProfileSurveysProjectTree over surveyToolDisplayOrder [command]); a command effect needs command offered"
	srcWorkerChildTools = "internal/toolpolicy/engine.go:ListForPrompt drops tools whose contract admits only worker children " +
		"(native-tools.yaml session_scope request_decision: worker_child) from a coordinator"
	srcCoordinatorBranch = "internal/session/workerworkspace/worker_tool_coord.go:OnWorkerBranch is false for a session without a parent"
)

// effectAtoms are the command effect flags; commandEffectFlags sets each only
// when command is offered.
var effectAtoms = []string{"profile_command_mutation", "profile_command_external_exec", "profile_command_network_egress"}

var rootAtoms = []string{"root_count", "has_workspace_roots"}

// withSkillDerived answers skill guards with skillDerived and others with
// next.
func withSkillDerived(next func(interactions.ConditionSite, string) independence.Constraints) func(interactions.ConditionSite, string) independence.Constraints {
	return func(site interactions.ConditionSite, recipient string) independence.Constraints {
		if site.Family == "skill" {
			return skillDerived(site, recipient)
		}
		return next(site, recipient)
	}
}

// skillDerived returns the production invariants over a skill guard's atoms
// on recipient, keeping only those whose atoms the guard reads.
func skillDerived(site interactions.ConditionSite, recipient string) independence.Constraints {
	var c independence.Constraints
	has := func(a string) bool { return slices.Contains(site.Atoms, a) }
	imply := func(ifs, thens []string, source string) {
		for _, i := range ifs {
			for _, t := range thens {
				if has(i) && has(t) {
					c.Implications = append(c.Implications, independence.Implication{If: i, Then: t, Source: source})
				}
			}
		}
	}
	switch recipient {
	case interactions.RecipientCoordinator:
		imply(append([]string{"profile_has_command", "profile_has_command_output", "profile_has_render_view", "profile_has_skills_read"}, effectAtoms...), rootAtoms, srcCoordinatorNoFolder)
		for _, f := range []independence.Fixed{
			{Atom: "profile_has_request_decision", Value: independence.False, Source: srcWorkerChildTools},
			{Atom: "on_worker_branch", Value: independence.False, Source: srcCoordinatorBranch},
		} {
			if has(f.Atom) {
				c.Fixed = append(c.Fixed, f)
			}
		}
	case interactions.RecipientWorker:
		imply(effectAtoms, rootAtoms, srcWorkerRoots)
	}
	return c
}
