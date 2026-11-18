package contract

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
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

func unreachableNames(t *testing.T, catalog *toolvocab.Catalog, corpus string, reader prompts.Reader) []string {
	t.Helper()
	return toolvocab.UnreachableNames(catalog, corpus, reader.Held())
}

func linesNaming(corpus string, names []string) string {
	var out []string
	for _, line := range strings.Split(corpus, "\n") {
		for _, name := range names {
			handle := strings.HasSuffix(name, "#") && strings.Contains(line, name)
			if handle || strings.Contains(line, "`"+name+"`") || strings.Contains(line, "`"+name+"(") {
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
	states := map[string]surface.ImplementSessionState{
		"idle":              {},
		"workers in flight": {WorkersInFlight: 2},
		"pending overlays":  {PendingOverlayIDs: []string{"job-overlay-1"}, WorkersInFlight: 1},
	}
	for _, rootCount := range []int{0, 1, 2} {
		plans, err := surface.CompileToolPlans(rootCount)
		contractcheck.FailErr(t, "compile tool plans", err)
		for id := range plans {
			reader := coordinatorTestReader(t, id, rootCount)
			check := func(t *testing.T, corpus string) {
				t.Helper()
				if names := unreachableNames(t, catalog, corpus, reader); len(names) > 0 {
					t.Errorf("names %s, which this surface cannot reach:\n%s", strings.Join(names, ", "), linesNaming(corpus, names))
				}
			}
			// With no folder attached, only the surfaces that reach no worktree open.
			if rootCount > 0 || slices.Contains(noFolderSurfaces, id) {
				t.Run(fmt.Sprintf("%s/%d-roots", id, rootCount), func(t *testing.T) {
					check(t, renderCoordinatorTripartiteAtRootCount(t, root, rootCount, id))
				})
			}
			if rootCount != 1 {
				continue
			}
			for name, state := range states {
				t.Run(id+"/"+name, func(t *testing.T) {
					check(t, renderCoordinatorTripartiteForRunContext(t, root, api.CoordinatorRunContext{},
						&api.Session{Posture: api.SessionPostureBuild}, surface.HostLoopWakeSentinel,
						WorkerCompletionHistory("implementer"), id, state))
				})
			}
		}
	}
}

var noFolderSurfaces = []string{surfaceImplementRouting, "implement_investigate", surfaceImplementSynthesis}

type workflowPhaseBindings struct {
	Injects []struct {
		On       string `yaml:"on"`
		Render   string `yaml:"render"`
		Selector struct {
			Phase string `yaml:"phase"`
		} `yaml:"selector"`
	} `yaml:"injects"`
	Phases []struct {
		ID      string `yaml:"id"`
		Surface string `yaml:"coordinator_surface"`
	} `yaml:"phases"`
}
