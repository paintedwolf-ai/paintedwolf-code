package contract

import (
	"context"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/agentdef"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolvocab"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func promptNameCatalog(t *testing.T) (*toolvocab.Catalog, []sandbox.ToolProfile) {
	t.Helper()
	eff := contractcheck.StockCatalog(t)
	profiles, err := sandbox.LoadToolProfilesWithCatalog(eff)
	contractcheck.FailErr(t, "load tool profiles", err)
	schemas, _, err := extpacks.LoadEffectiveToolSchemas(eff)
	contractcheck.FailErr(t, "load tool schemas", err)
	catalog, err := toolvocab.NewCatalog(schemas, profiles, toolvocab.Provenance{})
	contractcheck.FailErr(t, "toolvocab.NewCatalog", err)
	return catalog, profiles
}

func unreachableNames(t *testing.T, catalog *toolvocab.Catalog, corpus string, reachable []string) []string {
	t.Helper()
	language, err := toolcontract.ReadOnlyLanguageV1()
	contractcheck.FailErr(t, "load command language", err)
	program := func(name string) bool {
		return slices.ContainsFunc(language.Commands, func(spec toolcontract.CommandSpecification) bool { return spec.Name == name })
	}
	var out []string
	for _, name := range toolvocab.ToolsIn(catalog, corpus) {
		if slices.Contains(reachable, name) || (program(name) && slices.Contains(reachable, "command")) {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func linesNaming(corpus string, names []string) string {
	var out []string
	for _, line := range strings.Split(corpus, "\n") {
		for _, name := range names {
			if strings.Contains(line, "`"+name+"`") || strings.Contains(line, "`"+name+"(") {
				out = append(out, line)
				break
			}
		}
	}
	return strings.Join(out, "\n")
}

// A rendered prompt names a tool only when the surface it renders for can reach
// it. Retired names count: a prompt must not teach what no longer exists.
func TestRenderedCoordinatorPromptsNameOnlyReachableTools(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	catalog, _ := promptNameCatalog(t)
	plans, err := surface.CompileToolPlans(1)
	contractcheck.FailErr(t, "compile tool plans", err)
	for id, plan := range plans {
		t.Run(id, func(t *testing.T) {
			corpus := renderCoordinatorTripartiteForRunContext(t, root, api.CoordinatorRunContext{},
				&api.Session{Posture: api.SessionPostureBuild}, surface.HostLoopWakeSentinel,
				WorkerCompletionHistory("implementer"), id)
			if names := unreachableNames(t, catalog, corpus, plan.AddressableNames()); len(names) > 0 {
				t.Errorf("names %s, which this surface cannot reach:\n%s", strings.Join(names, ", "), linesNaming(corpus, names))
			}
		})
	}
}

func TestRenderedWorkerPersonasNameOnlyReachableTools(t *testing.T) {
	catalog, profiles := promptNameCatalog(t)
	eff := contractcheck.StockCatalog(t)
	agents, err := agentdef.LoadEffectiveWithCatalog(eff)
	contractcheck.FailErr(t, "load agents", err)
	engine := contractPersonaEngine(t)
	for _, agent := range agents {
		if agent.ID == "coordinator" {
			continue
		}
		t.Run(agent.ID, func(t *testing.T) {
			idx := slices.IndexFunc(profiles, func(p sandbox.ToolProfile) bool { return p.ID == agent.ToolProfile })
			if idx < 0 {
				t.Skip("agent holds no tool profile")
			}
			var reachable []string
			for name, enabled := range profiles[idx].Tools {
				if enabled {
					reachable = append(reachable, name)
				}
			}
			corpus, err := prompts.RenderPersona(context.Background(), engine, agent.ID, nil)
			contractcheck.FailErr(t, "render persona", err)
			if names := unreachableNames(t, catalog, corpus, reachable); len(names) > 0 {
				t.Errorf("names %s, which this profile does not hold:\n%s", strings.Join(names, ", "), linesNaming(corpus, names))
			}
		})
	}
}
