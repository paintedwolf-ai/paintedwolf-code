package contract

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
)

func TestRenderedPromptsWithinBudget(t *testing.T) {
	// Persona render caches depend on the active catalog identity.
	contractcheck.ActivateStockCatalog(t)

	root := contractcheck.RepoRoot(t)
	lycaonRoot := filepath.Join(root, "lycaon")
	reg, err := LoadPromptBudgetRegistry(lycaonRoot)
	contractcheck.FailErr(t, "LoadPromptBudgetRegistry", err)
	measured := measurePromptBudgets(t, root, reg)
	if os.Getenv("UPDATE_PROMPT_BUDGETS") == "1" {
		writePromptBudgets(t, lycaonRoot, measured)
		return
	}
	budgets, err := prompts.LoadPromptBudgets()
	contractcheck.FailErr(t, "LoadPromptBudgets", err)
	allCaps := map[string]map[string]int{
		"worker_personas":        budgets.WorkerPersonas,
		"coordinator_tripartite": budgets.CoordinatorTripartite,
		"coordinator_injects":    budgets.CoordinatorInjects,
		"agent_templates":        budgets.AgentTemplates,
		"kicks":                  budgets.Kicks,
		"tool_surfaces":          budgets.ToolSurfaces,
	}
	catalog := buildPromptBudgetCatalog(reg)
	if violations := collectPromptBudgetViolations(measured, allCaps); len(violations) > 0 {
		t.Fatal(formatPromptBudgetFailureReport(violations, catalog))
	}
}

func measurePromptBudgets(t *testing.T, root string, reg *PromptBudgetRegistry) promptBudgetMeasurements {
	t.Helper()
	if reg == nil {
		t.Fatal("PromptBudgetRegistry required")
	}
	engine := contractPersonaEngine(t)
	vars := mergeCoordinatorTemplateVars(t)

	out := promptBudgetMeasurements{
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

func writePromptBudgets(t *testing.T, lycaonRoot string, measured promptBudgetMeasurements) {
	t.Helper()
	path := filepath.Join(lycaonRoot, "config", "packs", "painted-wolf", "platform", "host", "prompt-budgets.yaml")
	// Manual limits carry through the refresh; only measured caps are rewritten.
	var cfg prompts.PromptBudgets
	if existing, err := prompts.LoadPromptBudgets(); err == nil {
		cfg = *existing
	}
	cfg.Version = 1
	cfg.WorkerPersonas = sortedIntMap(measured["worker_personas"])
	cfg.CoordinatorTripartite = sortedIntMap(measured["coordinator_tripartite"])
	cfg.CoordinatorInjects = sortedIntMap(measured["coordinator_injects"])
	cfg.AgentTemplates = sortedIntMap(measured["agent_templates"])
	cfg.Kicks = sortedIntMap(measured["kicks"])
	cfg.ToolSurfaces = sortedIntMap(measured["tool_surfaces"])
	assertStaticPromptStackWithinModelWindow(t, &cfg)
	raw, err := yaml.Marshal(&cfg)
	contractcheck.FailErr(t, "marshal prompt budgets", err)
	header := `# Rendered prompt caps measure UTF-8 bytes with fixture variables.
# Model ceilings, attachment caps, and the perception window are manual.
`
	if err := os.WriteFile(path, append([]byte(header), raw...), 0o644); err != nil {
		contractcheck.FailErr(t, "write prompt budgets", err)
	}
}

// TestPromptBudgetRefreshKeepsManualLimits holds every section the refresh
// does not measure across a rewrite.
func TestPromptBudgetRefreshKeepsManualLimits(t *testing.T) {
	existing, err := prompts.LoadPromptBudgets()
	contractcheck.FailErr(t, "LoadPromptBudgets", err)
	measured := promptBudgetMeasurements{
		"worker_personas":        existing.WorkerPersonas,
		"coordinator_tripartite": existing.CoordinatorTripartite,
		"coordinator_injects":    existing.CoordinatorInjects,
		"agent_templates":        existing.AgentTemplates,
		"kicks":                  existing.Kicks,
		"tool_surfaces":          existing.ToolSurfaces,
	}
	root := t.TempDir()
	host := filepath.Join(root, "config", "packs", "painted-wolf", "platform", "host")
	contractcheck.FailErr(t, "mkdir host", os.MkdirAll(host, 0o755))
	writePromptBudgets(t, root, measured)
	raw, err := os.ReadFile(filepath.Join(host, "prompt-budgets.yaml"))
	contractcheck.FailErr(t, "read refreshed budgets", err)
	var refreshed prompts.PromptBudgets
	contractcheck.FailErr(t, "decode refreshed budgets", yaml.Unmarshal(raw, &refreshed))
	if !reflect.DeepEqual(&refreshed, existing) {
		t.Fatalf("refresh changed unmeasured limits:\nbefore %+v\nafter  %+v", existing, &refreshed)
	}
}

// TestStaticPromptStackWithinModelWindow bounds the full static prompt stack.
func TestStaticPromptStackWithinModelWindow(t *testing.T) {
	t.Parallel()
	budgets, err := prompts.LoadPromptBudgets()
	contractcheck.FailErr(t, "LoadPromptBudgets", err)
	if os.Getenv("UPDATE_PROMPT_BUDGETS") == "1" {
		// The refresh writes after compilation; validate the new artifact rather
		// than the previous caps embedded in this test binary.
		path := filepath.Join(contractcheck.RepoRoot(t), "lycaon", "config", "packs", "painted-wolf", "platform", "host", "prompt-budgets.yaml")
		raw, err := os.ReadFile(path)
		contractcheck.FailErr(t, "read refreshed prompt budgets", err)
		budgets = &prompts.PromptBudgets{}
		contractcheck.FailErr(t, "decode refreshed prompt budgets", yaml.Unmarshal(raw, budgets))
	}
	assertStaticPromptStackWithinModelWindow(t, budgets)
}

func assertStaticPromptStackWithinModelWindow(t *testing.T, budgets *prompts.PromptBudgets) {
	t.Helper()
	am := budgets.AbsoluteMaximums
	if am == nil {
		t.Fatal("prompt-budgets.yaml: absolute_maximums missing")
	}
	ceilingTokens := am.StaticStackCeilingTokens()
	if ceilingTokens <= 0 {
		t.Fatalf("static stack ceiling must be positive (window=%d reserved=%d)", am.ModelContextWindowTokens, am.ReservedSessionTokens)
	}

	// Every artifact contributes its byte cap to the worst-case stack.
	coordBytes := promptBudgetMaxCap(budgets.CoordinatorTripartite) +
		budgets.ToolSurfaces["coordinator"] +
		promptBudgetSumCaps(budgets.CoordinatorInjects)
	workerBytes := promptBudgetMaxCap(budgets.WorkerPersonas) + promptBudgetMaxWorkerSurface(budgets.ToolSurfaces)

	stacks := []struct {
		name  string
		bytes int
	}{
		{"coordinator turn (max tripartite + coordinator tools + all injects)", coordBytes},
		{"worker turn (max persona + max worker tool surface)", workerBytes},
	}
	for _, s := range stacks {
		tokens := promptBudgetBytesToTokens(s.bytes)
		if tokens > ceilingTokens {
			t.Fatalf("%s: worst-case static stack ≈%d tokens (%d bytes) exceeds per-turn static ceiling %d tokens "+
				"(window %d − reserved %d) for %s.\n"+
				"Trim scaffolding (see prompt-budgets byte caps) or, if intentional, lower reserved_session_tokens — "+
				"but that leaves less room for transcript, tool tail, ledger, and output+reasoning.",
				s.name, tokens, s.bytes, ceilingTokens, am.ModelContextWindowTokens, am.ReservedSessionTokens, am.Model)
		}
	}
}

// TestCompactionBudgetWithinModelWindow keeps live budgets within the true window.
func TestCompactionBudgetWithinModelWindow(t *testing.T) {
	t.Parallel()

	budgets, err := prompts.LoadPromptBudgets()
	contractcheck.FailErr(t, "LoadPromptBudgets", err)
	am := budgets.AbsoluteMaximums
	if am == nil || am.ModelContextWindowTokens <= 0 {
		t.Fatal("prompt-budgets.yaml: absolute_maximums.model_context_window_tokens required as the true-window SSOT")
	}
	if strings.TrimSpace(am.Model) == "" {
		t.Fatal("prompt-budgets.yaml: absolute_maximums.model required")
	}

	windows, err := modelinfo.LoadModelContextWindows()
	contractcheck.FailErr(t, "LoadModelContextWindows", err)
	cfg := compaction.DefaultCompactionConfig()

	policy := llm.ModelPolicy{Coordinator: llm.ModelRef{Model: am.Model}}
	trueWindow, source := llm.ResolveTrueWindow(policy, nil, windows, cfg)
	if trueWindow != am.ModelContextWindowTokens {
		t.Errorf("absolute_maximums.model_context_window_tokens (%d) != ResolveTrueWindow(%q)=%d (source=%s)",
			am.ModelContextWindowTokens, am.Model, trueWindow, source)
	}

	live, _, err := llm.ApplyLiveBudget(cfg, policy, nil, windows)
	contractcheck.FailErr(t, "ApplyLiveBudget", err)
	if live.ModelContextWindow > trueWindow {
		t.Errorf("live_budget (%d) exceeds true_window (%d)", live.ModelContextWindow, trueWindow)
	}
	if live.HardCeilingTokens > trueWindow {
		t.Errorf("compaction hard ceiling (%d) exceeds true_window (%d)", live.HardCeilingTokens, trueWindow)
	}
}

func promptBudgetBytesToTokens(b int) int { return (b + 3) / 4 }

func promptBudgetMaxCap(m map[string]int) int {
	max := 0
	for _, v := range m {
		if v > max {
			max = v
		}
	}
	return max
}

func promptBudgetSumCaps(m map[string]int) int {
	sum := 0
	for _, v := range m {
		sum += v
	}
	return sum
}

// promptBudgetMaxWorkerSurface returns the largest worker tool surface.
func promptBudgetMaxWorkerSurface(surfaces map[string]int) int {
	max := 0
	for id, v := range surfaces {
		if id == "coordinator" {
			continue
		}
		if v > max {
			max = v
		}
	}
	return max
}

func sortedIntMap(in map[string]int) map[string]int {
	if len(in) == 0 {
		return map[string]int{}
	}
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make(map[string]int, len(in))
	for _, k := range keys {
		out[k] = in[k]
	}
	return out
}
