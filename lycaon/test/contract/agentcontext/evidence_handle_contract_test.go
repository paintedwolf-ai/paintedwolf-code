package contract

import (
	"fmt"
	"maps"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/promptunit"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolsurface"
	"github.com/lycaon/lycaon/internal/toolvocab"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Units render from tool facts, so every branch must be true for any tool set,
// not only for the stock surfaces, where command's sealed capture can mask a
// handle a held tool does not mint. The production selector picks the units for
// every tool alone and all tools together (each host and mode family), every
// coordinator surface and every tool profile; each renders with full host facts
// and may name only tools and evidence handles that set reaches.
func TestInstructionUnitsNameOnlyReachableToolsAndHandles(t *testing.T) {
	catalog, _ := promptNameCatalog(t)
	binding, err := evidence.LoadBinding()
	contractcheck.FailErr(t, "load evidence binding", err)
	engine := contractPersonaEngine(t)
	units, err := prompts.UnitCatalogForEngine(engine)
	contractcheck.FailErr(t, "unit catalog", err)
	base := mergeCoordinatorSurfaceVars(t, "implement_synthesis")
	mergeFixtureWorkspaceRoot(t, base, "")
	for _, state := range toolFactStates(t, binding, units) {
		allTools := map[string]bool{}
		for _, tool := range slices.Concat(state.offered, state.requestable) {
			allTools[tool] = true
		}
		deferred := map[string]bool{}
		for _, tool := range state.requestable {
			deferred[tool] = true
		}
		offered := map[string]bool{}
		for _, tool := range state.offered {
			offered[tool] = true
		}
		profileID := "synthetic"
		if state.host == promptunit.HostCoordinator {
			profileID = prompts.CoordinatorProfileID
		}
		reader, err := prompts.NewReader(prompts.ReaderInput{
			ProfileID: profileID,
			Mode:      state.mode,
			Profiles: []sandbox.ToolProfile{{
				ID:             profileID,
				Tools:          allTools,
				DeferredTools:  deferred,
				CommandEffects: toolcontract.AllCommandEffects(),
			}},
			Plan:      toolsurface.Compile(state.offered, state.requestable),
			Loaded:    offered,
			Visible:   slices.Concat(state.offered, state.requestable),
			RootCount: 1,
			HasGit:    true,
			WebSearch: true,
		})
		contractcheck.FailErr(t, "build state reader", err)
		vars, err := prompts.Vars(reader, maps.Clone(base))
		contractcheck.FailErr(t, "merge state vars", err)
		held := reader.Held()
		selection := reader.Selection()
		for _, slot := range units.Rendered(selection) {
			for _, unit := range slot {
				if !unit.Stock {
					continue
				}
				rendered, err := engine.RenderUnit(t.Context(), unit.Ref, maps.Clone(vars))
				contractcheck.FailErr(t, "render unit "+unit.ID+" for "+state.name, err)
				if names := toolvocab.UnreachableNames(catalog, rendered, held); len(names) > 0 {
					t.Errorf("unit %s [%s] names %v, which that tool set cannot reach or mint:\n%s", unit.ID, state.name, names, linesNaming(rendered, names))
				}
			}
		}
	}
}

type toolFactState struct {
	name                 string
	host                 promptunit.Host
	mode                 string
	offered, requestable []string
}

func toolFactStates(t *testing.T, binding *evidence.Binding, units promptunit.Catalog) []toolFactState {
	t.Helper()
	modes := []string{""}
	for _, unit := range units.Units() {
		for _, mode := range unit.Modes {
			if !slices.Contains(modes, mode) {
				modes = append(modes, mode)
			}
		}
	}
	all := binding.DeclaredToolNames()
	synthetic := []toolFactState{{name: "no tools"}, {name: "all tools", offered: all}}
	for _, tool := range all {
		synthetic = append(synthetic, toolFactState{name: tool + " alone", offered: []string{tool}})
	}
	var states []toolFactState
	for _, state := range synthetic {
		for _, host := range []promptunit.Host{promptunit.HostCoordinator, promptunit.HostWorker} {
			for _, mode := range modes {
				states = append(states, toolFactState{name: fmt.Sprintf("%s, %s, mode %q", state.name, host, mode),
					host: host, mode: mode, offered: state.offered})
			}
		}
	}
	plans, err := surface.CompileToolPlans(1)
	contractcheck.FailErr(t, "compile coordinator surfaces", err)
	for _, id := range slices.Sorted(maps.Keys(plans)) {
		states = append(states, toolFactState{name: "surface " + id, host: promptunit.HostCoordinator, mode: surface.ExecutionModeFamily(id),
			offered: plans[id].ImmediateNames(), requestable: plans[id].DeferredNames()})
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
		host := promptunit.HostWorker
		if profile.ID == "coordinator" {
			host = promptunit.HostCoordinator
		}
		states = append(states, toolFactState{name: "profile " + profile.ID, host: host, offered: offered, requestable: requestable})
	}
	return states
}
