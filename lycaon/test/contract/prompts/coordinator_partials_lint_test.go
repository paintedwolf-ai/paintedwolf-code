package contract

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/capability"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/pongoplain"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/spawn"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// coordinatorCoreVars assembles coordinator-core's variables for one surface the
// way the standing prompt does: reader facts, policy, capability and unit slots.
// supplied holds every key a builder produced, including keys the reader drops
// for this surface.
func coordinatorCoreVars(t *testing.T, surfaceID string, rootCount int) (vars map[string]any, supplied map[string]bool) {
	t.Helper()
	profiles, err := sandbox.LoadToolProfiles()
	contractcheck.FailErr(t, "LoadToolProfiles", err)
	plan, err := prompts.CompileSurfacePlan(surfaceID, rootCount)
	contractcheck.FailErr(t, "CompileSurfacePlan", err)
	reader, err := prompts.NewReader(prompts.ReaderInput{
		ProfileID: prompts.CoordinatorProfileID, SurfaceID: surfaceID, Plan: plan,
		Profiles: profiles, RootCount: rootCount, WebSearch: true,
	})
	contractcheck.FailErr(t, "NewReader", err)
	occ := spawn.PolicyTemplateVars()
	maps.Copy(occ, prompts.AgentPromptSurfaceTemplateVars(prompts.AgentPromptSurface{}))
	contractcheck.FailErr(t, "MergeForTurn", capability.MergeForTurn(occ, reader, spawn.AmbientAllowedAgents()))
	prompts.MergeUnitVars(occ, nil)
	supplied = map[string]bool{}
	for k := range occ {
		supplied[k] = true
	}
	vars, err = prompts.Vars(reader, occ)
	contractcheck.FailErr(t, "prompts.Vars", err)
	for k := range vars {
		supplied[k] = true
	}
	return vars, supplied
}

// Every variable coordinator-core reads is a declared reader fact or a key the
// standing-prompt builders supply, and the template renders for every surface
// at every root count; an unsupplied variable renders as a silent empty branch.
func TestCoordinatorCoreVarWhitelist(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "agents", "prompts", "coordinator-core.md"))
	contractcheck.FailErr(t, "read coordinator-core", err)
	refs := pongoplain.VariableReferences(string(raw))
	if len(refs) == 0 {
		t.Fatal("coordinator-core references no variables")
	}
	engine := contractcheck.BundledPromptEngineForRoot(t)
	rendered := 0
	for _, rootCount := range []int{0, 1, 2} {
		plans, err := surface.CompileToolPlans(rootCount)
		contractcheck.FailErr(t, "CompileToolPlans", err)
		ids := make([]string, 0, len(plans))
		for id := range plans {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			t.Run(fmt.Sprintf("%s/%d-roots", id, rootCount), func(t *testing.T) {
				vars, supplied := coordinatorCoreVars(t, id, rootCount)
				for _, ref := range refs {
					if !supplied[ref] && !prompts.IsDeclaredOrReservedReaderFact(ref) {
						t.Errorf("coordinator-core reads %q, which no reader fact or prompt builder supplies", ref)
					}
				}
				_, err := engine.Render(context.Background(), surface.CoordinatorCoreTemplate, vars)
				contractcheck.FailErr(t, "render coordinator-core", err)
			})
			rendered++
		}
	}
	if rendered == 0 {
		t.Fatal("no coordinator surfaces compiled")
	}
}
