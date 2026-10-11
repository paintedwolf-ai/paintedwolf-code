//go:build budgets

package contract

import (
	"github.com/lycaon/lycaon/internal/toolcontract"

	"cmp"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/promptunit"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/sizebudget"
)

// TestRenderedPromptsWithinBudget holds every shipped prompt to its category
// limit or its exception, and every turn kind's widest static prompt to the
// model window.
//
// Prompts are few, so the limit holds for every prompt, touched or not;
// warnings name only the prompts whose sources the change edited.
func TestRenderedPromptsWithinBudget(t *testing.T) {
	// Persona render caches depend on the active catalog identity.
	contractcheck.ActivateStockCatalog(t)

	root := contractcheck.RepoRoot(t)
	change, err := sizebudget.LoadChangeSet()
	contractcheck.FailErr(t, "load change set", err)
	lycaonRoot := filepath.Join(root, "lycaon")
	reg, err := LoadPromptBudgetRegistry(lycaonRoot)
	contractcheck.FailErr(t, "LoadPromptBudgetRegistry", err)
	measured := measurePromptBudgets(t, root, reg)
	raw, err := os.ReadFile(promptBudgetPolicyFile(root))
	contractcheck.FailErr(t, "read prompt budgets", err)
	policy, err := decodePromptSizePolicy(raw)
	contractcheck.FailErr(t, "decode prompt budgets", err)
	budgets, err := prompts.LoadPromptBudgets()
	contractcheck.FailErr(t, "LoadPromptBudgets", err)
	stacks := promptStacks(t, budgets.AbsoluteMaximums, measured)

	catalog := buildPromptBudgetCatalog(reg)
	sources := promptBudgetSources(catalog)
	edited := func(category, id string) bool { return change.TouchesAny(sources[category][id]) }
	var findings []sizebudget.Finding
	for _, f := range sizebudget.Evaluate(policy, measured.sizes, func(string, string) bool { return true }) {
		if f.Kind.Fails() || f.Kind == sizebudget.Unneeded || f.Kind == sizebudget.Vanished || edited(f.Category, f.ID) {
			findings = append(findings, f)
		}
	}
	var notes []string
	basePolicy, ok, note, err := sizebudget.BasePolicy(root, change.Base, promptBudgetPolicyPath, decodePromptSizePolicy)
	contractcheck.FailErr(t, "read base prompt budgets", err)
	if ok {
		findings = append(findings, sizebudget.ExceptionChanges(basePolicy, policy)...)
	} else {
		notes = append(notes, note)
	}
	report := sizebudget.NewReport(promptBudgetSuite, policy, findings, nil, sources)
	report.Notes = append(report.Notes, notes...)
	// Low headroom is a standing fact; it warns when this change edits prompts.
	promptsEdited := false
	for category, ids := range sources {
		for id := range ids {
			promptsEdited = promptsEdited || edited(category, id)
		}
	}
	for _, stack := range widestStacks(stacks) {
		if stack.Headroom() < staticHeadroomWarnPercent && promptsEdited {
			report.Warnings = append(report.Warnings, stack.String()+fmt.Sprintf("; below the %d%% warning line", staticHeadroomWarnPercent))
		} else {
			report.Notes = append(report.Notes, "widest "+stack.String())
		}
	}
	contractcheck.FailErr(t, "write prompt budget report", sizebudget.WriteReport(report))
	if failures := sizebudget.Failures(findings); len(failures) > 0 {
		t.Error(sizebudget.FailureReport(promptBudgetSuite, failures, promptBudgetDetail(catalog)))
	}
	for _, stack := range stacks {
		if stack.Tokens() > stack.Ceiling {
			t.Errorf("%s exceeds the static ceiling (window %d − reserved %d) for %s.\n"+
				"This is the widest turn: every mandatory unit rendered, as when the decision engine abstains, and one request's largest tool loads. "+
				"Trim its largest part, or lower reserved_session_tokens and accept less room for transcript, tool tail, ledger, and output.",
				stack, budgets.AbsoluteMaximums.ModelContextWindowTokens, budgets.AbsoluteMaximums.ReservedSessionTokens, budgets.AbsoluteMaximums.Model)
		}
	}
}

// promptMeasurements holds every measured prompt artifact, and the parts a
// turn's static stack is assembled from.
type promptMeasurements struct {
	sizes    sizebudget.Measurements
	surfaces toolSurfaceMeasurements
	units    promptunit.Catalog
	// tripartiteSurfaces maps each tripartite fixture to its coordinator surface.
	tripartiteSurfaces map[string]string
}

func measurePromptBudgets(t *testing.T, root string, reg *PromptBudgetRegistry) promptMeasurements {
	t.Helper()
	if reg == nil {
		t.Fatal("PromptBudgetRegistry required")
	}
	engine := contractPersonaEngine(t)
	vars := mergeCoordinatorTemplateVars(t)

	out := sizebudget.Measurements{
		"worker_personas":        {},
		"coordinator_tripartite": {},
		"coordinator_injects":    {},
		"agent_templates":        {},
		"kicks":                  {},
		"units":                  {},
	}
	surfaces := measureToolSurfaces(t)
	out["coordinator_tool_surfaces"], out["worker_tool_surfaces"] = map[string]int{}, map[string]int{}
	for id, schemas := range surfaces.Coordinator {
		out["coordinator_tool_surfaces"][id] = schemas.Upfront
	}
	for id, schemas := range surfaces.Workers {
		out["worker_tool_surfaces"][id] = schemas.Upfront
	}
	units, err := prompts.UnitCatalogForEngine(engine)
	contractcheck.FailErr(t, "unit catalog", err)
	for _, unit := range units.Units() {
		if !unit.Stock {
			continue
		}
		rendered, err := engine.RenderUnit(context.Background(), unit.Ref, vars)
		contractcheck.FailErr(t, "render unit "+unit.ID, err)
		out["units"][unit.ID] = len(strings.TrimSpace(rendered))
	}
	tripartiteSurfaces := map[string]string{}
	for _, id := range reg.WorkerPersonaIDs() {
		personaExtra := map[string]any{}
		set := reg.AgentSkills[id]
		mergePromptBudgetSkillRosterForSet(t, personaExtra, set)
		if !set.Empty() {
			requirePromptBudgetSkillAvailability(t, personaExtra, "worker persona "+id)
		}
		if writeAgentForBudget(t, id) {
			mergePromptBudgetHostResources(t, personaExtra)
			requirePromptBudgetHostResources(t, personaExtra, "worker persona "+id)
		}
		rendered, err := prompts.RenderPersona(context.Background(), engine, id, personaExtra)
		contractcheck.FailErr(t, "RenderPersona "+id, err)
		out["worker_personas"][id] = len(rendered)
	}
	for _, row := range reg.Tripartite {
		forced := ""
		if row.SurfaceID == toolcontract.SurfaceImplementInvestigate {
			forced = row.SurfaceID
		}
		rendered := renderCoordinatorTripartiteForRunContext(t, root, row.RunCtx, row.Sess, row.UserPrompt, row.History, forced, row.State)
		// Investigate surfaces include the skill-reading procedure.
		if row.SurfaceID == toolcontract.SurfaceImplementInvestigate && !strings.Contains(rendered, "## Skills") {
			t.Fatalf("%s measured without the skill-reading procedure", row.Name)
		}
		if !strings.Contains(rendered, "## Host resources") {
			t.Fatalf("%s measured without the bundled host-resource inventory; caps would understate the shipped prompt", row.Name)
		}
		out["coordinator_tripartite"][row.Name] = len(rendered)
		tripartiteSurfaces[row.Name] = row.SurfaceID
	}
	measureCoordinatorInjectSizes(t, root, engine, out["coordinator_injects"])
	for id, ref := range reg.AgentTemplates {
		renderVars := vars
		if writeAgentForBudget(t, id) {
			renderVars = map[string]any{}
			for k, v := range vars {
				renderVars[k] = v
			}
			mergePromptBudgetHostResources(t, renderVars)
			requirePromptBudgetHostResources(t, renderVars, "agent template "+id)
		}
		rendered, err := engine.Render(context.Background(), ref, renderVars)
		contractcheck.FailErr(t, "render agent template "+id, err)
		out["agent_templates"][id] = len(rendered)
	}
	for _, id := range reg.KickIDs {
		kickVars := map[string]any{}
		for k, v := range vars {
			kickVars[k] = v
		}
		if id == "coordinator-security-challenge" {
			kickVars["review_followup_attempts"] = 2
		}
		// Kick caps include phase obligations.
		kickVars["failed_leaves"] = []string{"hitl_consulted:intake"}
		kickVars["gate_obligations"] = []map[string]any{{
			"id":      "hitl_consulted:intake",
			"purpose": "Plan intake is freeform must-consult.",
			"satisfy": []string{
				"Call ask_user with task-appropriate questions when no host card is pending.",
				"When a host question card is already announced, wait for the card — do not re-ask in chat.",
				"After the user resolves an ask_user card, the host stamps hitl_consulted:intake; keep asking if more forks remain.",
				"When consultation is complete, call workflow_advance (gates stay blocked until then if the leaf is still unsatisfied).",
			},
		}}
		if id == "coordinator-process-finished" {
			kickVars["command_completion"] = strings.Repeat("x", 5400)
		}
		if id == "coordinator-process-refused" {
			kickVars["command_refusal"] = strings.Repeat("x", 5400)
		}
		if id == "coordinator-roots-changed" {
			before := []projectroot.RootRef{{ID: "source", Label: "source", Path: "/workspace/source", IsPrimary: true}}
			after := []projectroot.RootRef{{ID: "source", Label: "source", Path: "/workspace/source"}, {ID: "assets", Label: "assets", Path: "/workspace/assets", IsPrimary: true}}
			kickVars = prompts.RootsChangedKickData(before, after, "source")
		}
		rendered, err := engine.Render(context.Background(), "kicks/"+id+".md", kickVars)
		contractcheck.FailErr(t, "render kick "+id, err)
		out["kicks"][id] = len(rendered)
	}
	return promptMeasurements{sizes: out, surfaces: surfaces, units: units, tripartiteSurfaces: tripartiteSurfaces}
}

func promptBudgetWorkerLegFixture() inject.WorkerLegContext {
	return inject.WorkerLegContext{
		LegID:              "leg-1",
		AgentType:          "path-explorer",
		PhaseID:            "implement",
		CompletionCriteria: []string{"tests pass", "docs updated"},
		LegTools:           []string{"read", "grep", "find", "list_dir"},
		Checklist:          []string{"survey repo", "cite paths", "finish handoff"},
	}
}

func promptBudgetActiveWorkflowFixture() api.CoordinatorRunContext {
	return api.CoordinatorRunContext{
		WorkflowID:       "plan",
		CurrentPhase:     "research",
		RunID:            "run-1",
		RunStatus:        "running",
		CoordinatorBrief: "Use pack topology.",
		FailedLeaves:     []string{"research_satisfied"},
		FeedbackPhases:   []api.ComposeFeedbackPhase{{ID: "clarify", Prompt: "pick db"}},
	}
}

// promptBudgetWorkflowSnapshotFixture covers every workflow phase shape.
func promptBudgetWorkflowSnapshotFixture() inject.WorkflowRuntimeSnapshot {
	return inject.WorkflowRuntimeSnapshot{
		Topology: "default-pipeline",
		Phases: []inject.WorkflowPhaseRow{
			{ID: "intake", CompleteWhen: "gates_satisfied", Next: "research"},
			{ID: "research", CompleteWhen: "gates_satisfied", Next: "expand",
				Gates: []inject.WorkflowGateState{{ID: "research_satisfied", Satisfied: false}}},
			{ID: "expand", CompleteWhen: "gates_satisfied", Next: "review"},
			{ID: "review", CompleteWhen: "gates_satisfied", Next: "approve"},
			{ID: "approve", CompleteWhen: "gates_satisfied", Next: "build"},
			{ID: "build", CompleteWhen: "gates_satisfied", Next: "done"},
			{ID: "done", Terminal: true},
		},
	}
}

// staticHeadroomWarnPercent is the share of the static ceiling below which
// every run warns, because the next prompt growth could reach the window.
const staticHeadroomWarnPercent = 10

// promptStack is the widest static prompt one turn kind carries: every
// mandatory unit renders, as when the decision engine abstains, and one
// request_tools load has added its largest schemas. Kicks and project content
// (AGENTS.md chains, MCP schemas, source briefs, skill bodies) ride in the
// reserved session budget, which compaction calibrates against the provider's
// token counts.
type promptStack struct {
	Kind                          string
	System, Tools, Units, Injects int
	// AllLoaded is the tool schema size once every requestable tool loaded,
	// which warm turns can approach before the next cold boundary.
	AllLoaded int
	Ceiling   int
}

// Bytes is the stack's UTF-8 size.
func (s promptStack) Bytes() int { return s.System + s.Tools + s.Units + s.Injects }

// Tokens estimates conservatively at four bytes per token; multi-byte
// characters count more than once.
func (s promptStack) Tokens() int { return (s.Bytes() + 3) / 4 }

// Headroom is the share of the static ceiling left unused, in percent.
func (s promptStack) Headroom() float64 {
	return 100 * float64(s.Ceiling-s.Tokens()) / float64(s.Ceiling)
}

func (s promptStack) String() string {
	return fmt.Sprintf("%s: ≈%d of %d static tokens, %.1f%% headroom (system %d, tool schemas %d, loaded-tool units %d, injects %d bytes; "+
		"every requestable tool loaded: tool schemas %d bytes)",
		s.Kind, s.Tokens(), s.Ceiling, s.Headroom(), s.System, s.Tools, s.Units, s.Injects, s.AllLoaded)
}

// promptStacks assembles each turn kind's widest static prompt from its own
// parts: a coordinator turn per tripartite fixture with its surface's tools,
// and a worker turn per persona with its profile's tools. Each adds only the
// injects that can ride that kind of turn, and one request's largest loads.
func promptStacks(t *testing.T, am *prompts.AbsoluteMaximums, m promptMeasurements) []promptStack {
	t.Helper()
	if am == nil {
		t.Fatal("prompt-budgets.yaml: absolute_maximums missing")
	}
	ceiling := am.StaticStackCeilingTokens()
	if ceiling <= 0 {
		t.Fatalf("static stack ceiling must be positive (window=%d reserved=%d)", am.ModelContextWindowTokens, am.ReservedSessionTokens)
	}
	decisions, err := turnload.LoadCatalog()
	contractcheck.FailErr(t, "load turn decisions", err)
	maxLoads := decisions.Request.MaxLoads
	injects := map[promptunit.Host]int{}
	for _, spec := range PromptBudgetInjectMatrix {
		for _, host := range spec.Hosts {
			injects[host] += m.sizes["coordinator_injects"][spec.ID]
		}
	}
	var stacks []promptStack
	for fixture, system := range m.sizes["coordinator_tripartite"] {
		surfaceID := m.tripartiteSurfaces[fixture]
		schemas, ok := m.surfaces.Coordinator[surfaceID]
		if !ok {
			t.Fatalf("tripartite fixture %s names surface %q, which no coordinator surface plan compiles", fixture, surfaceID)
		}
		// The tripartite render offers every loadable tool, so the units of
		// whatever loads are already in the system prompt.
		loads := schemas.Largest(maxLoads)
		stacks = append(stacks, promptStack{Kind: "coordinator turn " + fixture, System: system,
			Tools: schemas.Upfront + schemas.Loads(loads), Injects: injects[promptunit.HostCoordinator],
			AllLoaded: schemas.AllLoaded(), Ceiling: ceiling})
	}
	for persona, system := range m.sizes["worker_personas"] {
		profile, err := prompts.ToolProfileForAgent(persona)
		if err != nil {
			profile = persona
		}
		schemas, ok := m.surfaces.Workers[profile]
		if !ok {
			t.Fatalf("worker persona %s uses tool profile %q, which was not measured", persona, profile)
		}
		// A persona renders its upfront tools' units; a loaded tool brings its own.
		loads := schemas.Largest(maxLoads)
		loaded := map[string]bool{}
		for _, name := range loads {
			loaded[name] = true
		}
		units := 0
		for _, unit := range m.units.Units() {
			if unit.Stock && unit.HostsFor(promptunit.HostWorker) && unit.Slot != promptunit.SlotProcedures &&
				unit.AttachedTo(loaded) && !unit.AttachedTo(schemas.upfront) {
				units += m.sizes["units"][unit.ID]
			}
		}
		stacks = append(stacks, promptStack{Kind: "worker turn " + persona, System: system,
			Tools: schemas.Upfront + schemas.Loads(loads), Units: units, Injects: injects[promptunit.HostWorker],
			AllLoaded: schemas.AllLoaded(), Ceiling: ceiling})
	}
	slices.SortFunc(stacks, func(a, b promptStack) int { return cmp.Or(b.Bytes()-a.Bytes(), strings.Compare(a.Kind, b.Kind)) })
	return stacks
}

// widestStacks returns the widest coordinator and worker stacks.
func widestStacks(stacks []promptStack) []promptStack {
	var out []promptStack
	seen := map[bool]bool{}
	for _, stack := range stacks {
		coordinator := strings.HasPrefix(stack.Kind, "coordinator")
		if !seen[coordinator] {
			seen[coordinator] = true
			out = append(out, stack)
		}
	}
	return out
}
