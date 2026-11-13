package contract

import (
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/toolvocab"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Units render from tool facts, so every branch must be true for any tool set,
// not only for the stock surfaces, where command's sealed capture can mask a
// handle a held tool does not mint. Each stock unit renders with full host facts
// for every tool alone, every coordinator surface, every tool profile and all
// tools together, and may name only tools and evidence handles that set reaches.
func TestInstructionUnitsNameOnlyReachableToolsAndHandles(t *testing.T) {
	catalog, _ := promptNameCatalog(t)
	binding, err := evidence.LoadBinding()
	contractcheck.FailErr(t, "load evidence binding", err)
	engine := contractPersonaEngine(t)
	units, err := prompts.UnitCatalogForEngine(engine)
	contractcheck.FailErr(t, "unit catalog", err)
	base := mergeCoordinatorTemplateVars(t)
	for _, state := range toolFactStates(t, binding) {
		vars := maps.Clone(base)
		maps.Copy(vars, prompts.VisibleToolsNativePartialVars(state.offered))
		prompts.MergeVisualShowVars(state.offered, state.requestable, vars)
		held := map[string]bool{}
		for _, tool := range slices.Concat(state.offered, state.requestable) {
			held[tool] = true
		}
		if held["command"] && vars["profile_command_mutation"] != false {
			held[toolvocab.ReachCommandMutation] = true
		}
		for _, unit := range units.Units() {
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

// policyHandle matches an evidence handle in policy copy.
var policyHandle = regexp.MustCompile(`(?:^|[^A-Za-z0-9_/.:-])([a-z][a-z0-9_]*)#`)

// Policy copy renders per occurrence from facts that do not carry the reader's
// tools, so each handle it names must at least be a declared evidence kind.
func TestPolicyCopyNamesDeclaredEvidenceHandles(t *testing.T) {
	binding, err := evidence.LoadBinding()
	contractcheck.FailErr(t, "load evidence binding", err)
	kinds := binding.KindShapeMap()
	packs := filepath.Join(contractcheck.RepoRoot(t), "lycaon", "config", "packs")
	var files []string
	for _, pattern := range []string{"*/*/policy/*.yaml", "*/*/host/next-steps/*.yaml"} {
		matches, err := filepath.Glob(filepath.Join(packs, pattern))
		contractcheck.FailErr(t, "glob "+pattern, err)
		files = append(files, matches...)
	}
	if len(files) == 0 {
		t.Fatal("no policy copy found")
	}
	checked := 0
	for _, path := range files {
		body, err := os.ReadFile(path)
		contractcheck.FailErr(t, "read "+path, err)
		for _, match := range policyHandle.FindAllStringSubmatch(string(body), -1) {
			checked++
			if _, known := kinds[match[1]]; !known && binding.ToolKind(match[1]) != "" {
				rel, _ := filepath.Rel(packs, path)
				t.Errorf("%s: names tool %q as an evidence handle; its kind is %q", rel, match[1]+"#", binding.ToolKind(match[1])+"#")
			}
		}
	}
	if checked == 0 {
		t.Fatal("no handle found in policy copy; the scan is broken")
	}
}
