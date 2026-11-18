package promptrender

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/agentdef"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	sessionprofiles "github.com/lycaon/lycaon/internal/session/profiles"
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/internal/toolvocab"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowpresentation "github.com/lycaon/lycaon/internal/workflow/presentation"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/contract/laws"
)

// reachRules hold invariant P13: rendered guidance names only the tools,
// command programs and arguments its reader is offered. The oracle is the
// reader's held set, which the templates do not produce.
var reachRules = []laws.Rule{
	{Name: "coordinator-kicks-name-only-offered-tools", Anchors: []laws.Anchor{kickRenderer}, Check: coordinatorKicksNameOnlyOffered},
	{Name: "phase-kicks-name-only-their-surface-tools", Anchors: []laws.Anchor{kickRenderer, phaseSurfaceResolver}, Check: phaseKicksNameOnlyTheirSurface},
	{Name: "worker-kicks-name-only-offered-tools", Anchors: []laws.Anchor{kickRenderer}, Check: workerKicksNameOnlyOffered},
	{Name: "worker-personas-name-only-offered-tools", Anchors: []laws.Anchor{personaRenderer}, Check: workerPersonasNameOnlyOffered},
	{Name: "skills-name-only-offered-tools", Anchors: []laws.Anchor{laws.Func("internal/toolvocab", "SkillToolsOffered")}, Check: skillsNameOnlyOffered},
	{Name: "reject-copy-names-only-schema-fields", Anchors: []laws.Anchor{laws.Func("internal/guidance", "NewStaticRejectFormatter")}, Check: rejectCopyNamesOnlySchemaFields},
	{Name: "phase-exits-are-callable-on-their-surface", Anchors: []laws.Anchor{phaseSurfaceResolver, laws.Func("internal/workflow/presentation", "ProjectPhaseExit")}, Check: phaseExitsAreCallable},
}

var phaseSurfaceResolver = laws.Func("internal/coordinator/surface", "EnrichRunContextForWorkflow")

// phaseKickPrefixes render only on their workflow phase, whose surface the
// phase declares; the phase rule covers them.
var phaseKickPrefixes = []string{
	"coordinator-debate-", "coordinator-fanout-", "coordinator-phase-exit", "coordinator-plan-", "coordinator-recon-",
	"coordinator-review-loop-", "coordinator-security-",
}

// reportUnoffered reports each name text uses that held does not reach, and
// each argument text attributes to a tool, as `tool(arg=…)` or `tool.arg`,
// that the tool's schema lacks.
func reportUnoffered(r *laws.Run, label, text string, held map[string]bool) {
	s := stock(r)
	for _, name := range toolvocab.UnreachableNames(s.Names, text, held) {
		r.Report("%s names %s, which its reader is not offered: %s", label, name, lineNaming(text, name))
	}
	schema := func(tool string) (map[string]any, bool) {
		meta, ok := s.Schemas.ToolMeta(tool)
		return meta.ArgsSchema, ok
	}
	for _, claim := range toolvocab.UnacceptedArgs(s.Names, text, schema) {
		r.Report("%s gives %s argument `%s`, which its schema lacks: %s", label, claim.Tool, claim.Arg, lineNaming(text, claim.Arg))
	}
}

func lineNaming(text, name string) string {
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, "`"+name+"`") || strings.Contains(line, "`"+name+"(") || strings.Contains(line, name) {
			return fmt.Sprintf("%q", strings.TrimSpace(line))
		}
	}
	return "(no single line)"
}

// kickData is the full kick fixture without the host digest, which quotes
// observations the reader already holds.
func kickData() map[string]any {
	data := KickFixture()
	delete(data, "evidence_digest")
	return data
}

// informKicks lists the inform bindings' renders that start with prefix.
func informKicks(r *laws.Run, prefix string) []string {
	reg, err := anchor.LoadRegistryFromConfigRoot()
	r.Must(err)
	var ids []string
	for _, b := range reg.AllInform() {
		if b != nil && b.IsInform() && strings.HasPrefix(b.Render, prefix) && !slices.Contains(ids, b.Render) {
			ids = append(ids, b.Render)
		}
	}
	slices.Sort(ids)
	return ids
}

// A wake can reach any surface, so a coordinator kick names only what that
// surface's plan offers.
func coordinatorKicksNameOnlyOffered(r *laws.Run) {
	s := stock(r)
	plans, err := surface.CompileToolPlans(1)
	r.Must(err)
	engine := s.Engine()
	checked := 0
	for _, kick := range informKicks(r, "coordinator-") {
		if slices.ContainsFunc(phaseKickPrefixes, func(p string) bool { return strings.HasPrefix(kick, p) }) {
			continue
		}
		for _, id := range sortedKeys(plans) {
			reader, err := CoordinatorReader(s, id, 1)
			r.Must(err)
			data := kickData()
			delete(data, "tools")
			prompts.MergeCoordinatorKickVarsForTools(data, reader)
			text, err := engine.RenderKick(context.Background(), kick, data)
			r.Must(err)
			checked++
			reportUnoffered(r, fmt.Sprintf("kick %s on %s", kick, id), text, reader.Held())
		}
	}
	r.Expect(checked, "coordinator kick renders")
}

// Phase guidance opens on the surface its workflow declares for that phase.
func phaseKicksNameOnlyTheirSurface(r *laws.Run) {
	s := stock(r)
	plans, err := surface.CompileToolPlans(1)
	r.Must(err)
	reg, err := workflowdef.RegistryFromDirs("")
	r.Must(err)
	engine := s.Engine()
	checked := 0
	for _, key := range sortedKeys(reg.All()) {
		m := reg.All()[key]
		surfaces := map[string]string{}
		for _, phase := range m.PhaseDefs {
			surfaces[phase.ID] = phase.CoordinatorSurface
		}
		for _, in := range m.Injects {
			if in.On != "phase.entered" || in.Selector.Phase == nil {
				continue
			}
			id := surfaces[*in.Selector.Phase]
			if _, known := plans[id]; !known {
				continue
			}
			reader, err := CoordinatorReader(s, id, 1)
			r.Must(err)
			data := kickData()
			prompts.MergeCoordinatorKickVarsForTools(data, reader)
			text, err := engine.RenderKick(context.Background(), in.Render, data)
			r.Must(err)
			checked++
			reportUnoffered(r, fmt.Sprintf("%s phase %s kick %s on %s", key, *in.Selector.Phase, in.Render, id), text, reader.Held())
		}
	}
	r.Expect(checked, "phase kick renders")
}

// postureProfiles narrows every profile the way the sandbox does for a
// session in each posture.
func postureProfiles(r *laws.Run) map[string][]sandbox.ToolProfile {
	postures, err := sessionprofiles.LoadPostureRegistry()
	r.Must(err)
	out := map[string][]sandbox.ToolProfile{}
	for _, posture := range postures.List() {
		narrowed := slices.Clone(stock(r).Profiles)
		for i := range narrowed {
			narrowed[i].CommandEffects &= posture.CommandEffects
		}
		out[string(posture.ID)] = narrowed
	}
	r.Expect(len(out), "session postures")
	return out
}

// workerReader is a worker reader for one profile under one posture.
func workerReader(r *laws.Run, profileID string, profiles []sandbox.ToolProfile) prompts.Reader {
	reader, err := prompts.NewReader(prompts.ReaderInput{
		ProfileID: profileID, Profiles: profiles, RootCount: 1, HasGit: true, WebSearch: true,
	})
	r.Must(err)
	return reader
}

// A worker kick reaches every worker profile under every posture, so it
// names only what each holds.
func workerKicksNameOnlyOffered(r *laws.Run) {
	s := stock(r)
	engine := s.Engine()
	checked := 0
	for posture, profiles := range postureProfiles(r) {
		for _, profile := range profiles {
			if !profile.Tools["complete_leg"] {
				continue
			}
			reader := workerReader(r, profile.ID, profiles)
			for _, kick := range informKicks(r, "worker-") {
				data, err := prompts.Vars(reader, kickData())
				r.Must(err)
				text, err := engine.RenderKick(context.Background(), kick, data)
				r.Must(err)
				checked++
				reportUnoffered(r, fmt.Sprintf("kick %s for %s in %s", kick, profile.ID, posture), text, reader.Held())
			}
		}
	}
	r.Expect(checked, "worker kick renders")
}

// Every worker persona names only what its profile holds under every
// posture.
func workerPersonasNameOnlyOffered(r *laws.Run) {
	s := stock(r)
	agents, err := agentdef.LoadEffectiveWithCatalog(s.Catalog)
	r.Must(err)
	engine := s.Engine()
	checked := 0
	for posture, profiles := range postureProfiles(r) {
		for _, agent := range agents {
			if agent.ID == "coordinator" || !slices.ContainsFunc(profiles, func(p sandbox.ToolProfile) bool { return p.ID == agent.ToolProfile }) {
				continue
			}
			reader := workerReader(r, agent.ToolProfile, profiles)
			text, err := prompts.RenderPersona(context.Background(), engine, agent.ID, reader, nil)
			r.Must(err)
			checked++
			reportUnoffered(r, fmt.Sprintf("persona %s in %s", agent.ID, posture), text, reader.Held())
		}
	}
	r.Expect(checked, "worker persona renders")
}

// Every stock skill, rendered for every reader that can select it, names
// only what that reader holds.
func skillsNameOnlyOffered(r *laws.Run) {
	renders := skillRenders(r)
	r.Expect(len(renders), "stock skill renders")
	for _, sr := range renders {
		texts := make([]string, len(sr.Parts))
		for i, part := range sr.Parts {
			texts[i] = part.Text
		}
		reportUnoffered(r, fmt.Sprintf("skill %s as read by %s", sr.Skill.Name, sr.Reader), strings.Join(texts, "\n"), sr.State.Held())
	}
}

// backtickedIdent matches a backticked argument name or dotted path.
var backtickedIdent = regexp.MustCompile("`([A-Za-z_][A-Za-z0-9_]*(?:\\.[A-Za-z_][A-Za-z0-9_]*)*)`")

// fieldIndex is the argument paths each tool's schema accepts.
type fieldIndex struct {
	byTool map[string]map[string]bool
	union  map[string]bool
}

func newFieldIndex(schemas *toolschema.Config) fieldIndex {
	idx := fieldIndex{byTool: map[string]map[string]bool{}, union: map[string]bool{}}
	for name := range schemas.Tools {
		meta, ok := schemas.ToolMeta(name)
		if !ok {
			continue
		}
		fields := map[string]bool{}
		for _, path := range toolschema.ArgFieldPaths(meta.ArgsSchema) {
			// A dotted path also names its head.
			head, _, _ := strings.Cut(path, ".")
			for _, p := range []string{path, head} {
				fields[p] = true
				idx.union[p] = true
			}
		}
		idx.byTool[name] = fields
	}
	return idx
}

// unschemaed lists the argument names text attributes to tool that neither
// tool nor any other tool the text names accepts. A backticked word no
// schema accepts is not read as an argument.
func (idx fieldIndex) unschemaed(tool, text string) []string {
	named := map[string]bool{tool: true}
	var claims []string
	for _, m := range backtickedIdent.FindAllStringSubmatch(text, -1) {
		head, _, _ := strings.Cut(m[1], ".")
		if _, isTool := idx.byTool[head]; isTool {
			named[head] = true
		} else if idx.union[m[1]] {
			claims = append(claims, m[1])
		}
	}
	var bad []string
	for _, claim := range claims {
		if !slices.ContainsFunc(sortedKeys(named), func(t string) bool { return idx.byTool[t][claim] }) && !slices.Contains(bad, claim) {
			bad = append(bad, claim)
		}
	}
	slices.Sort(bad)
	return bad
}

// Every rejection card, rendered for each tool its selector names, names
// only arguments that tool's schema accepts.
func rejectCopyNamesOnlySchemaFields(r *laws.Run) {
	idx := newFieldIndex(stock(r).Schemas)
	cfg := laws.HintConfig(r.Corpus)
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(stock(r).Engine()))
	formatter := guidance.NewStaticRejectFormatter(cfg)
	checked := 0
	for _, code := range sortedKeys(cfg.HintCodes) {
		entry := cfg.HintCodes[code]
		for _, tool := range entry.Tools {
			tool = strings.TrimSpace(tool)
			if _, ok := idx.byTool[tool]; !ok {
				continue
			}
			checked++
			texts := []string{entry.What, entry.Cause, entry.Why, entry.Fix, entry.Instead}
			for _, sc := range entry.Scenarios {
				vars := guidance.ScenarioVars(entry, sc)
				vars["tool"] = tool
				// The selector rules report a card that does not render.
				if rendered, err := formatter.Format(code, vars); err == nil {
					texts = append(texts, rendered)
				}
			}
			for _, field := range idx.unschemaed(tool, strings.Join(texts, "\n")) {
				r.Report("%s rendered for %s names `%s`, which %s does not accept", code, tool, field, tool)
			}
		}
	}
	r.Expect(checked, "rejection cards with a tool selector")
}

// phaseExitCall matches a non-negated instruction to call a tool.
var phaseExitCall = regexp.MustCompile("((?i:do not |never ))?call `([a-z_]+)`")

// Every non-terminal phase's leave channels are addressable on the surface
// it binds, and its rendered exit steps call only tools that surface offers.
func phaseExitsAreCallable(r *laws.Run) {
	s := stock(r)
	reg, err := workflowdef.RegistryFromDirs("")
	r.Must(err)
	plans, err := surface.CompileToolPlans(1)
	r.Must(err)
	coordinator := slices.IndexFunc(s.Profiles, func(p sandbox.ToolProfile) bool { return p.ID == prompts.CoordinatorProfileID })
	if coordinator < 0 {
		r.Fatalf("the coordinator tool profile is not declared")
	}
	grants := s.Profiles[coordinator].Tools
	engine := s.Engine()
	moduleRoot := r.Corpus.Path("lycaon")
	checked := 0
	for _, key := range sortedKeys(reg.All()) {
		m := reg.All()[key]
		for _, phase := range m.PhaseDefs {
			id := strings.TrimSpace(phase.CoordinatorSurface)
			if id == "" {
				id = strings.TrimSpace(surface.EnrichRunContextForWorkflow(api.CoordinatorRunContext{
					WorkflowID: m.ID, CurrentPhase: phase.ID, SurfaceProfile: m.SurfaceProfile,
				}, moduleRoot).PhaseCoordinatorSurface)
			}
			plan, ok := plans[id]
			if !ok {
				continue
			}
			checked++
			wire := plan.AddressableNames()
			label := fmt.Sprintf("%s phase %s on %s", key, phase.ID, id)
			for _, channel := range phaseExitChannels(m, phase) {
				if !slices.Contains(wire, channel.tool) {
					r.Report("%s needs %s (%s), which that surface does not offer", label, channel.tool, channel.why)
				}
			}
			vars := inject.ActiveWorkflowInjectToMap(inject.ActiveWorkflowInjectData{
				WorkflowID: "laws", CurrentPhase: "p",
				PhaseExit: workflowpresentation.ProjectPhaseExit(m, phase, nil, nil).InjectView(),
				Phases:    []inject.ActiveWorkflowPhaseView{{ID: "p", IsCurrent: true}},
			}, nil, nil)
			text, err := engine.Render(context.Background(), "guidance/active-workflow.md", vars)
			r.Must(err)
			seen := map[string]bool{}
			for _, match := range phaseExitCall.FindAllStringSubmatch(text, -1) {
				name := match[2]
				if match[1] != "" || seen[name] || !grants[name] || slices.Contains(wire, name) {
					continue
				}
				seen[name] = true
				r.Report("%s exit step calls `%s`, which that surface does not offer", label, name)
			}
		}
	}
	r.Expect(checked, "workflow phases bound to a surface")
}

// exitChannel is a tool a phase's declared leave path needs.
type exitChannel struct{ tool, why string }

func phaseExitChannels(m workflowdef.Manifest, phase workflowdef.PhaseDef) []exitChannel {
	if phase.Terminal {
		return nil
	}
	var out []exitChannel
	if workflowdef.EffectiveAdvancePolicy(m, phase) == workflowdef.AdvanceWhenGateMetCoordinator {
		out = append(out, exitChannel{"workflow_advance", "advance.when_gate_met: coordinator"})
	}
	if phase.ReviewLoop != nil {
		out = append(out, exitChannel{"submit_verdict", "review_loop verdict"})
	}
	if slices.ContainsFunc(phase.Gates, func(g string) bool { return strings.HasPrefix(g, "hitl_consulted") }) {
		out = append(out, exitChannel{"ask_user", "a hitl_consulted gate"})
	}
	for _, tr := range phase.Transitions {
		if slices.Contains(tr.Actors, workflowdef.TransitionActorCoordinator) {
			out = append(out, exitChannel{"workflow_transition", "coordinator transition " + tr.ID})
		}
	}
	return out
}
