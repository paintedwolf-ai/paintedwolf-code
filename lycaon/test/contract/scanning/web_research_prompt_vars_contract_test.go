package contract

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/capability"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surfacecatalog"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/webresearch"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// With web search off, no coordinator surface offers a web tool or the web
// researcher in its rendered prompt; with it on, some surface does.
func TestWebResearchPromptVarsSuppliedAndGated(t *testing.T) {
	t.Parallel()
	profiles, err := sandbox.LoadToolProfiles()
	contractcheck.FailErr(t, "LoadToolProfiles", err)
	catalog, err := surfacecatalog.Load()
	contractcheck.FailErr(t, "load surface catalog", err)
	surfaces := catalog.SurfaceIDs()
	if len(surfaces) == 0 {
		t.Fatal("surface catalog lists no surfaces")
	}
	engine := contractcheck.BundledPromptEngineForRoot(t)
	webIDs := []string{webresearch.SearchToolName, webresearch.FetchURLToolName, webResearcherAgent}
	offeredWhenEnabled := false
	for _, surfaceID := range surfaces {
		for _, rootCount := range []int{0, 1} {
			for _, enabled := range []bool{false, true} {
				plan, err := prompts.CompileSurfacePlan(surfaceID, rootCount)
				contractcheck.FailErr(t, "CompileSurfacePlan "+surfaceID, err)
				reader, err := prompts.NewReader(prompts.ReaderInput{
					ProfileID: prompts.CoordinatorProfileID,
					SurfaceID: surfaceID,
					Plan:      plan,
					Profiles:  profiles,
					WebSearch: enabled,
				})
				contractcheck.FailErr(t, "NewReader "+surfaceID, err)
				vars := map[string]any{}
				contractcheck.FailErr(t, "MergeForTurn "+surfaceID, capability.MergeForTurn(vars, reader,
					inject.ResolveAgentRoster(surfaceID, spawn.AmbientAllowedAgents(), rootCount, false, enabled).Effective))
				vars, err = prompts.Vars(reader, vars)
				contractcheck.FailErr(t, "prompts.Vars "+surfaceID, err)
				if vars["web_search_enabled"] != enabled {
					t.Fatalf("%s roots=%d: web_search_enabled = %v, want %t", surfaceID, rootCount, vars["web_search_enabled"], enabled)
				}
				rendered, err := engine.Render(t.Context(), "agents/coordinator-core.md", vars)
				contractcheck.FailErr(t, "render coordinator-core "+surfaceID, err)
				for _, id := range webIDs {
					mentioned := strings.Contains(rendered, id)
					if !enabled && mentioned {
						t.Errorf("%s roots=%d with web search off offers %s", surfaceID, rootCount, id)
					}
					if enabled && mentioned {
						offeredWhenEnabled = true
					}
				}
			}
		}
	}
	if !offeredWhenEnabled {
		t.Fatal("no surface offers a web tool or the web researcher with web search on")
	}
}
