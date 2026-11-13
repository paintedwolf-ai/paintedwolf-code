package contract

import (
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/nativemanifest"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/toolcontract"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// handleToken matches an evidence handle as copy writes it: kind#, kind#N.
var handleToken = regexp.MustCompile(`(?:^|[^A-Za-z0-9_/.-])([a-z][a-z0-9_]*)#`)

// Every evidence handle a shipped prompt names must be one a tool reachable on
// that turn can mint, so copy never sends a model to cite a kind it cannot hold.
// Each coordinator fixture is checked as a model receives it: system prompt and
// procedures message against its surface's tools; each worker persona against
// its tool profile.
func TestShippedPromptsNameOnlyReachableEvidenceHandles(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	binding, err := evidence.LoadBinding()
	contractcheck.FailErr(t, "load evidence binding", err)
	manifest, err := nativemanifest.Load()
	contractcheck.FailErr(t, "load native manifest", err)
	reg, err := LoadPromptBudgetRegistry(filepath.Join(root, "lycaon"))
	contractcheck.FailErr(t, "load prompt fixtures", err)
	plans, err := surface.CompileToolPlans(1)
	contractcheck.FailErr(t, "compile coordinator surfaces", err)
	renderer := prompts.NewInjectRenderer(contractPersonaEngine(t))
	if len(reg.Tripartite) == 0 || len(reg.WorkerPersonaIDs()) == 0 {
		t.Fatal("no prompt fixtures to check")
	}
	for _, row := range reg.Tripartite {
		plan, ok := plans[row.SurfaceID]
		if !ok {
			t.Fatalf("%s: surface %q has no plan", row.Name, row.SurfaceID)
		}
		forced := ""
		if row.SurfaceID == toolcontract.SurfaceImplementInvestigate {
			forced = row.SurfaceID
		}
		system := renderCoordinatorTripartiteForRunContext(t, root, row.RunCtx, row.Sess, row.UserPrompt, row.History, forced, row.State)
		procedures, err := inject.RenderToolProceduresBlock(t.Context(), renderer, "sess-handles", "coordinator", plan.ImmediateNames(), nil)
		contractcheck.FailErr(t, "render procedures for "+row.Name, err)
		reachable := reachableKinds(binding, manifest, slices.Concat(plan.ImmediateNames(), plan.DeferredNames()))
		checkEvidenceHandles(t, binding, row.Name+" system", system, reachable)
		checkEvidenceHandles(t, binding, row.Name+" procedures", procedures, reachable)
	}
	agents := orchestration.NewMemoryAgentRegistry()
	contractcheck.FailErr(t, "load agents", orchestration.LoadRequiredAgentRegistry(t.Context(), agents))
	profiles, err := sandbox.LoadToolProfiles()
	contractcheck.FailErr(t, "load tool profiles", err)
	byID := map[string]sandbox.ToolProfile{}
	for _, profile := range profiles {
		byID[profile.ID] = profile
	}
	engine := contractPersonaEngine(t)
	for _, id := range reg.WorkerPersonaIDs() {
		agent, err := agents.Get(id)
		contractcheck.FailErr(t, "agent "+id, err)
		profile, ok := byID[agent.ToolProfile]
		if !ok {
			t.Fatalf("persona %s: tool profile %q not found", id, agent.ToolProfile)
		}
		var tools []string
		for name, allowed := range profile.Tools {
			if allowed {
				tools = append(tools, name)
			}
		}
		rendered, err := prompts.RenderPersona(t.Context(), engine, id, map[string]any{})
		contractcheck.FailErr(t, "render persona "+id, err)
		checkEvidenceHandles(t, binding, "persona "+id, rendered, reachableKinds(binding, manifest, tools))
	}
}

// Units render from tool facts, so each branch must be true for any tool set,
// not only the stock surfaces: every stock unit is rendered with full host facts
// for each tool alone, every coordinator surface, every tool profile, and all
// tools together.
func TestInstructionUnitsNameOnlyReachableEvidenceHandles(t *testing.T) {
	binding, err := evidence.LoadBinding()
	contractcheck.FailErr(t, "load evidence binding", err)
	manifest, err := nativemanifest.Load()
	contractcheck.FailErr(t, "load native manifest", err)
	engine := contractPersonaEngine(t)
	units, err := prompts.UnitCatalogForEngine(engine)
	contractcheck.FailErr(t, "unit catalog", err)
	base := mergeCoordinatorTemplateVars(t)
	for _, state := range toolFactStates(t, binding) {
		vars := maps.Clone(base)
		maps.Copy(vars, prompts.VisibleToolsNativePartialVars(state.offered))
		prompts.MergeVisualShowVars(state.offered, state.requestable, vars)
		reachable := reachableKinds(binding, manifest, slices.Concat(state.offered, state.requestable))
		for _, unit := range units.Units() {
			if !unit.Stock {
				continue
			}
			rendered, err := engine.RenderUnit(t.Context(), unit.Ref, maps.Clone(vars))
			contractcheck.FailErr(t, "render unit "+unit.ID+" for "+state.name, err)
			checkEvidenceHandles(t, binding, "unit "+unit.ID+" ["+state.name+"]", rendered, reachable)
		}
	}
}

type toolFactState struct {
	name                 string
	offered, requestable []string
}

func toolFactStates(t *testing.T, binding *evidence.Binding) []toolFactState {
	t.Helper()
	all := binding.DeclaredToolNames()
	states := []toolFactState{{name: "no tools"}, {name: "all tools", offered: all}}
	for _, tool := range all {
		states = append(states, toolFactState{name: tool + " alone", offered: []string{tool}})
	}
	plans, err := surface.CompileToolPlans(1)
	contractcheck.FailErr(t, "compile coordinator surfaces", err)
	for _, id := range slices.Sorted(maps.Keys(plans)) {
		states = append(states, toolFactState{name: "surface " + id, offered: plans[id].ImmediateNames(), requestable: plans[id].DeferredNames()})
	}
	profiles, err := sandbox.LoadToolProfiles()
	contractcheck.FailErr(t, "load tool profiles", err)
	for _, profile := range profiles {
		var offered, requestable []string
		for _, tool := range slices.Sorted(maps.Keys(profile.Tools)) {
			switch {
			case !profile.Tools[tool]:
			case profile.DeferredTools[tool]:
				requestable = append(requestable, tool)
			default:
				offered = append(offered, tool)
			}
		}
		states = append(states, toolFactState{name: "profile " + profile.ID, offered: offered, requestable: requestable})
	}
	return states
}

func checkEvidenceHandles(t *testing.T, binding *evidence.Binding, where, text string, reachable map[string]bool) {
	t.Helper()
	kinds := binding.KindShapeMap()
	seen := map[string]bool{}
	for _, match := range handleToken.FindAllStringSubmatch(text, -1) {
		kind := match[1]
		if seen[kind] {
			continue
		}
		seen[kind] = true
		if _, known := kinds[kind]; !known {
			if binding.ToolKind(kind) != "" {
				t.Errorf("%s: names tool %q as an evidence handle; its kind is %q", where, kind+"#", binding.ToolKind(kind)+"#")
			}
			continue
		}
		if !reachable[kind] {
			t.Errorf("%s: names %q, which no tool reachable on that turn mints", where, kind+"#")
		}
	}
}

// Skill and policy copy is not rendered per tool set, so every handle it names
// must at least be a declared evidence kind.
func TestStaticCopyNamesDeclaredEvidenceHandles(t *testing.T) {
	binding, err := evidence.LoadBinding()
	contractcheck.FailErr(t, "load evidence binding", err)
	kinds := binding.KindShapeMap()
	packs := filepath.Join(contractcheck.RepoRoot(t), "lycaon", "config", "packs")
	var files []string
	for _, pattern := range []string{"*/*/skills/*/SKILL.md", "*/*/skills/*/references/*.md", "*/*/policy/*.yaml", "*/*/host/next-steps/*.yaml"} {
		matches, err := filepath.Glob(filepath.Join(packs, pattern))
		contractcheck.FailErr(t, "glob "+pattern, err)
		files = append(files, matches...)
	}
	if len(files) == 0 {
		t.Fatal("no skill or policy copy found")
	}
	for _, path := range files {
		body, err := os.ReadFile(path)
		contractcheck.FailErr(t, "read "+path, err)
		for _, match := range handleToken.FindAllStringSubmatch(string(body), -1) {
			kind := match[1]
			if _, known := kinds[kind]; !known && binding.ToolKind(kind) != "" {
				rel, _ := filepath.Rel(packs, path)
				t.Errorf("%s: names tool %q as an evidence handle; cite its kind %q", rel, kind+"#", binding.ToolKind(kind)+"#")
			}
		}
	}
}

// reachableKinds includes what command mints by running native operations,
// such as read# from cat.
func reachableKinds(binding *evidence.Binding, manifest nativemanifest.Config, tools []string) map[string]bool {
	out := map[string]bool{}
	for _, tool := range tools {
		names := []string{tool}
		if tool == "command" {
			for _, group := range manifest.Operations {
				names = append(names, group...)
			}
		}
		for _, name := range names {
			for _, kind := range binding.ProducibleKinds(name) {
				out[kind] = true
			}
		}
	}
	return out
}
