package hoststate_test

import (
	"slices"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/hoststate"
)

func TestHostStateEnumeratesAllPosturesSurfacesAndAgents(t *testing.T) {
	overlays, err := hoststate.DiscoverPackOverlays(t.Context())
	contractcheck.FailErr(t, "DiscoverPackOverlays", err)
	if len(overlays) != 5 {
		t.Fatalf("len(overlays) = %d, want 5 (stock + 4 single-pack removals)", len(overlays))
	}

	postures, err := hoststate.Postures(overlays[0].Catalog)
	contractcheck.FailErr(t, "Postures", err)
	var postureIDs []string
	for _, p := range postures {
		postureIDs = append(postureIDs, string(p.ID))
	}
	wantPostures := []string{"build", "orchestrate", "spec", "vet"}
	if !slices.Equal(postureIDs, wantPostures) {
		t.Fatalf("Postures = %v, want %v", postureIDs, wantPostures)
	}

	surfaces, err := hoststate.CoordinatorSurfaces()
	contractcheck.FailErr(t, "CoordinatorSurfaces", err)
	if len(surfaces) < 22 {
		t.Fatalf("len(CoordinatorSurfaces) = %d, want >= 22", len(surfaces))
	}
	requiresAgentCount := 0
	for _, s := range surfaces {
		if s.Surface.RequiresAgent {
			requiresAgentCount++
		}
	}
	if requiresAgentCount == 0 || requiresAgentCount == len(surfaces) {
		t.Fatalf("unexpected RequiresAgent partition: %d of %d", requiresAgentCount, len(surfaces))
	}

	workers, err := hoststate.WorkerAgents(overlays[0].Catalog)
	contractcheck.FailErr(t, "WorkerAgents", err)
	if len(workers) < 10 {
		t.Fatalf("len(WorkerAgents) = %d, want >= 10", len(workers))
	}

	coordStates, err := hoststate.CoordinatorStates(t.Context())
	contractcheck.FailErr(t, "CoordinatorStates", err)
	reachedSurfaces := map[string]bool{}
	excludedReasons := map[string]int{}
	hasMixedGitFirst, hasMixedNonGitFirst := false, false
	for _, st := range coordStates {
		if st.Excluded {
			excludedReasons[st.ExcludedReason]++
			continue
		}
		reachedSurfaces[st.TurnProfile.SurfaceID] = true
		if st.RootTopology == hoststate.RootsDualGitFirst && st.HasGit && st.HasAnyGitRoot {
			hasMixedGitFirst = true
		}
		if st.RootTopology == hoststate.RootsDualNonGitFirst && !st.HasGit && st.HasAnyGitRoot {
			hasMixedNonGitFirst = true
		}
	}
	if len(reachedSurfaces) != 22 {
		t.Fatalf("CoordinatorStates reached %d surfaces, want all 22: %v", len(reachedSurfaces), reachedSurfaces)
	}
	if !hasMixedGitFirst || !hasMixedNonGitFirst {
		t.Fatalf("mixed 2-root Git orders missing: gitFirst=%t nonGitFirst=%t", hasMixedGitFirst, hasMixedNonGitFirst)
	}
	if excludedReasons["requires_agent_surface_rejected_in_ask_mode"] == 0 {
		t.Fatalf("expected explicit exclusion reason for requires_agent in Ask mode: %v", excludedReasons)
	}

	workerStates, err := hoststate.WorkerStates(t.Context())
	contractcheck.FailErr(t, "WorkerStates", err)
	if len(workerStates) == 0 {
		t.Fatal("WorkerStates returned empty slice")
	}

	specs, err := hoststate.AnchorCatalogSpecs()
	contractcheck.FailErr(t, "AnchorCatalogSpecs", err)
	if len(specs) == 0 {
		t.Fatal("AnchorCatalogSpecs returned empty map")
	}
	for anchorID, spec := range specs {
		envs, envErr := hoststate.AnchorEnvelopes(anchorID)
		contractcheck.FailErr(t, "AnchorEnvelopes "+anchorID, envErr)
		if len(envs) == 0 {
			t.Fatalf("AnchorEnvelopes(%s) returned 0 maps", anchorID)
		}
		if len(spec.Vars) > 0 && len(envs) < 2 {
			t.Fatalf("AnchorEnvelopes(%s) returned %d map(s) despite %d declared vars", anchorID, len(envs), len(spec.Vars))
		}
	}
}

func TestFolderlessSurfacesMatchTurnResolver(t *testing.T) {
	got, err := hoststate.FolderlessSurfaces()
	contractcheck.FailErr(t, "FolderlessSurfaces", err)
	want := []string{
		"implement_investigate",
		"implement_routing",
		"implement_synthesis",
		"observe_investigate",
		"observe_synthesis",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("FolderlessSurfaces() = %v, want %v", got, want)
	}
}
