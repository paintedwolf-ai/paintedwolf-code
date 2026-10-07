//go:build budgets

package contract

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/sizebudget"
)

// TestRenderedPromptsWithinBudget holds every shipped prompt to its category
// limit and the static prompt stack to the model window.
func TestRenderedPromptsWithinBudget(t *testing.T) {
	// Persona render caches depend on the active catalog identity.
	contractcheck.ActivateStockCatalog(t)

	root := contractcheck.RepoRoot(t)
	lycaonRoot := filepath.Join(root, "lycaon")
	reg, err := LoadPromptBudgetRegistry(lycaonRoot)
	contractcheck.FailErr(t, "LoadPromptBudgetRegistry", err)
	measured := measurePromptBudgets(t, root, reg)
	file := promptBudgetPolicyFile(root)
	raw, err := os.ReadFile(file)
	contractcheck.FailErr(t, "read prompt budgets", err)
	policy, err := decodePromptSizePolicy(raw)
	contractcheck.FailErr(t, "decode prompt budgets", err)
	if os.Getenv("UPDATE_PROMPT_BUDGETS") == "1" {
		policy = sizebudget.Tighten(policy, measured)
		tightened, err := tightenPromptBudgetFile(raw, policy.Grandfathered)
		contractcheck.FailErr(t, "tighten prompt budgets", err)
		contractcheck.FailErr(t, "write prompt budgets", os.WriteFile(file, tightened, 0o644))
	}
	findings := sizebudget.Evaluate(policy, measured)
	base, ok, note, err := sizebudget.BasePolicy(root, promptBudgetPolicyPath, decodePromptSizePolicy)
	contractcheck.FailErr(t, "read base prompt budgets", err)
	var notes []string
	if ok {
		findings = append(findings, sizebudget.GrandfatherGrowth(base, policy)...)
	} else {
		notes = append(notes, note)
	}
	budgets, err := prompts.LoadPromptBudgets()
	contractcheck.FailErr(t, "LoadPromptBudgets", err)
	stacks, err := staticPromptStacks(budgets.AbsoluteMaximums, measured)
	contractcheck.FailErr(t, "static prompt stack", err)
	for _, stack := range stacks {
		notes = append(notes, stack.String())
	}
	catalog := buildPromptBudgetCatalog(reg)
	report := sizebudget.NewReport(promptBudgetSuite, policy, measured, promptBudgetSources(catalog), findings, notes)
	contractcheck.FailErr(t, "write prompt budget report", sizebudget.WriteReport(report))
	if failures := sizebudget.Failures(findings); len(failures) > 0 {
		t.Error(sizebudget.FailureReport(promptBudgetSuite, failures, promptBudgetDetail(catalog)))
	}
	for _, stack := range stacks {
		if stack.Tokens > stack.Ceiling {
			t.Errorf("%s exceeds the static ceiling %d tokens (window %d − reserved %d) for %s.\n"+
				"Trim the largest contributors, or lower reserved_session_tokens and accept less room for transcript, tool tail, ledger, and output.",
				stack, stack.Ceiling, budgets.AbsoluteMaximums.ModelContextWindowTokens, budgets.AbsoluteMaximums.ReservedSessionTokens, budgets.AbsoluteMaximums.Model)
		}
	}
}

func measurePromptBudgets(t *testing.T, root string, reg *PromptBudgetRegistry) sizebudget.Measurements {
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
	}
	out["tool_surfaces"] = measureToolSurfaceSizes(t)
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
		if row.SurfaceID == tools.SurfaceImplementInvestigate {
			forced = row.SurfaceID
		}
		rendered := renderCoordinatorTripartiteForRunContext(t, root, row.RunCtx, row.Sess, row.UserPrompt, row.History, forced, row.State)
		// Investigate surfaces include the skill-reading procedure.
		if row.SurfaceID == tools.SurfaceImplementInvestigate && !strings.Contains(rendered, "## Skills") {
			t.Fatalf("%s measured without the skill-reading procedure", row.Name)
		}
		if !strings.Contains(rendered, "## Host resources") {
			t.Fatalf("%s measured without the bundled host-resource inventory; caps would understate the shipped prompt", row.Name)
		}
		out["coordinator_tripartite"][row.Name] = len(rendered)
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
	return out
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

// staticPromptStack is the worst-case static prompt one turn kind carries.
type staticPromptStack struct {
	Name          string
	Bytes, Tokens int
	Ceiling       int
}

func (s staticPromptStack) String() string {
	headroom := 100 * float64(s.Ceiling-s.Tokens) / float64(s.Ceiling)
	return fmt.Sprintf("%s: ≈%d of %d static tokens (%.1f%% headroom)", s.Name, s.Tokens, s.Ceiling, headroom)
}

// staticPromptStacks sizes the largest coordinator and worker turns from the
// measured prompts, so the model window bounds what actually ships.
func staticPromptStacks(am *prompts.AbsoluteMaximums, measured sizebudget.Measurements) ([]staticPromptStack, error) {
	if am == nil {
		return nil, fmt.Errorf("prompt-budgets.yaml: absolute_maximums missing")
	}
	ceiling := am.StaticStackCeilingTokens()
	if ceiling <= 0 {
		return nil, fmt.Errorf("static stack ceiling must be positive (window=%d reserved=%d)", am.ModelContextWindowTokens, am.ReservedSessionTokens)
	}
	surfaces := measured["tool_surfaces"]
	workerSurface := 0
	for id, size := range surfaces {
		if id != "coordinator" {
			workerSurface = max(workerSurface, size)
		}
	}
	injects := 0
	for _, size := range measured["coordinator_injects"] {
		injects += size
	}
	stacks := []staticPromptStack{
		{Name: "coordinator turn (largest tripartite + coordinator tools + all injects)", Bytes: largest(measured["coordinator_tripartite"]) + surfaces["coordinator"] + injects},
		{Name: "worker turn (largest persona + largest worker tool surface)", Bytes: largest(measured["worker_personas"]) + workerSurface},
	}
	for i := range stacks {
		stacks[i].Tokens, stacks[i].Ceiling = (stacks[i].Bytes+3)/4, ceiling
	}
	return stacks, nil
}

func largest(sizes map[string]int) int {
	out := 0
	for _, size := range sizes {
		out = max(out, size)
	}
	return out
}
