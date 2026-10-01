package contract

import (
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/coordinator/surfacecatalog"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/toolcontract"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/workflowfixture"
)

// The read-only postures are the ones the flow table dispatches on, so the set
// derives from the table. Both forbidden axes are generated from
// native-tools.yaml, so a tool added there is covered without editing this test.
func TestReadOnlyPostureSurfacesCarryNoMutatingTool(t *testing.T) {
	t.Parallel()
	table, err := surface.ShippedCoordinatorFlowTable()
	contractcheck.FailErr(t, "load coordinator flow table", err)
	catalog, err := surfacecatalog.Load()
	contractcheck.FailErr(t, "load coordinator surfaces", err)
	manifests, err := workflowfixture.LoadMergedWorkflowCatalog(t)
	contractcheck.FailErr(t, "load workflow catalog", err)

	postures := readOnlyPostures(table)
	if len(postures) == 0 {
		t.Fatal("no read-only posture dispatch in the coordinator flow table")
	}
	for _, posture := range postures {
		reachable := flowReachableSurfaces(table, posture)
		for id := range manifestBoundSurfaces(manifests, posture) {
			reachable[id] = struct{}{}
		}
		if len(reachable) == 0 {
			t.Errorf("posture %q dispatches read-only but reaches no surface", posture)
		}
		for _, id := range sortedKeys(reachable) {
			row, err := catalog.Surface(id)
			contractcheck.FailErr(t, "load surface "+id, err)
			for _, tool := range surfaceTools(row) {
				if reason := forbiddenReadOnlyTool(tool); reason != "" {
					t.Errorf("posture %q reaches surface %q, which offers %q (%s)", posture, id, tool, reason)
				}
			}
		}
	}
}

// forbiddenReadOnlyTool names why a tool cannot sit on a read-only surface.
func forbiddenReadOnlyTool(tool string) string {
	switch {
	case settings.IsPathMutatingTool(tool):
		return "mutates a path its arguments declare"
	case settings.IsCommandToolName(tool):
		return "runs a program the model chooses"
	default:
		return ""
	}
}

func readOnlyPostures(table surface.FlowTable) []string {
	seen := map[string]struct{}{}
	collect := func(out surface.FlowOutput) {
		if strings.TrimSpace(out.DispatchOn) != surface.FactPosture {
			return
		}
		for posture := range out.Cases {
			seen[strings.TrimSpace(posture)] = struct{}{}
		}
	}
	for _, rule := range table.Rules {
		collect(rule.Out)
	}
	collect(table.Default)
	return sortedKeys(seen)
}

// flowReachableSurfaces resolves every row's destination at one posture.
// use_fact rows resolve to a manifest-bound surface, which the manifest pass
// covers directly.
func flowReachableSurfaces(table surface.FlowTable, posture string) map[string]struct{} {
	out := map[string]struct{}{}
	add := func(o surface.FlowOutput) {
		if id := strings.TrimSpace(o.SurfaceID); id != "" {
			out[id] = struct{}{}
			return
		}
		if strings.TrimSpace(o.DispatchOn) == "" {
			return
		}
		if o.DispatchOn == surface.FactPosture {
			if id, ok := o.Cases[posture]; ok {
				out[strings.TrimSpace(id)] = struct{}{}
				return
			}
		}
		if id := strings.TrimSpace(o.Default); id != "" {
			out[id] = struct{}{}
		}
	}
	for _, rule := range table.Rules {
		add(rule.Out)
	}
	add(table.Default)
	return out
}

func manifestBoundSurfaces(manifests map[string]workflowdef.Manifest, posture string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, m := range manifests {
		if !manifestRunsAtPosture(m, posture) {
			continue
		}
		for _, phase := range m.PhaseDefs {
			if id := strings.TrimSpace(phase.CoordinatorSurface); id != "" {
				out[id] = struct{}{}
			}
		}
	}
	return out
}

func manifestRunsAtPosture(m workflowdef.Manifest, posture string) bool {
	if strings.TrimSpace(m.InitialPosture) == posture {
		return true
	}
	for _, phase := range m.PhaseDefs {
		if strings.TrimSpace(phase.OnEnter.SetPosture) == posture {
			return true
		}
	}
	return false
}

func surfaceTools(row surfacecatalog.Surface) []string {
	return append(toolcontract.ExpandFamilies(row.Floor), row.LoadableTools()...)
}

func sortedKeys(in map[string]struct{}) []string {
	out := make([]string, 0, len(in))
	for k := range in {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
