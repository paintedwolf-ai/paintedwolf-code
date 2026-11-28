package hoststate_test

import (
	"slices"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/hoststate"
)

func TestHostStateEnumeratesAllPosturesSurfacesAndAgents(t *testing.T) {
	t.Cleanup(hoststate.HoldAuthorityMemos())
	overlays, err := hoststate.DiscoverPackOverlays(t.Context())
	contractcheck.FailErr(t, "DiscoverPackOverlays", err)
	if len(overlays) < 5 || overlays[0].ID != "stock" {
		t.Fatalf("len(overlays) = %d (first=%q), want stock + dynamically discovered behavioral pack removals", len(overlays), overlays[0].ID)
	}
	overlayIDs := make([]string, len(overlays))
	for i, ov := range overlays {
		overlayIDs[i] = ov.ID
	}
	for _, must := range []string{"without-browser", "without-implement", "without-plan", "without-recon-pack", "without-security", "without-web-research"} {
		if !slices.Contains(overlayIDs, must) {
			t.Fatalf("DiscoverPackOverlays missing %q in %v", must, overlayIDs)
		}
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
	coordApprovals := map[string]bool{}
	hasMixedGitFirst, hasMixedNonGitFirst, hasDualNeitherGit := false, false, false
	for _, st := range coordStates {
		if st.Excluded {
			excludedReasons[st.ExcludedReason]++
			continue
		}
		reachedSurfaces[st.TurnProfile.SurfaceID] = true
		coordApprovals[string(st.ApprovalPosture)] = true
		if st.RootTopology == hoststate.RootsDualGitFirst && st.HasGit && st.HasAnyGitRoot {
			hasMixedGitFirst = true
		}
		if st.RootTopology == hoststate.RootsDualNonGitFirst && !st.HasGit && st.HasAnyGitRoot {
			hasMixedNonGitFirst = true
		}
		if st.RootTopology == hoststate.RootsDualNeitherGit && !st.HasGit && !st.HasAnyGitRoot {
			hasDualNeitherGit = true
		}
	}
	if len(reachedSurfaces) != 22 {
		t.Fatalf("CoordinatorStates reached %d surfaces, want all 22: %v", len(reachedSurfaces), reachedSurfaces)
	}
	if !hasMixedGitFirst || !hasMixedNonGitFirst || !hasDualNeitherGit {
		t.Fatalf("2-root Git pairs missing: gitFirst=%t nonGitFirst=%t neitherGit=%t",
			hasMixedGitFirst, hasMixedNonGitFirst, hasDualNeitherGit)
	}
	for _, app := range []string{"balanced", "light", "strict"} {
		if !coordApprovals[app] {
			t.Fatalf("CoordinatorStates missing approval posture %q in %v", app, coordApprovals)
		}
	}
	if excludedReasons["requires_agent_surface_rejected_in_ask_mode"] == 0 {
		t.Fatalf("expected explicit exclusion reason for requires_agent in Ask mode: %v", excludedReasons)
	}

	workerStates, err := hoststate.WorkerStates(t.Context())
	contractcheck.FailErr(t, "WorkerStates", err)
	if len(workerStates) == 0 {
		t.Fatal("WorkerStates returned empty slice")
	}
	workerApprovals := map[string]bool{}
	hasWorkerDualNeither, hasWorkerDualSecondary, hasWorkerPartlyLoaded := false, false, false
	for _, ws := range workerStates {
		if ws.Excluded {
			continue
		}
		workerApprovals[string(ws.ApprovalPosture)] = true
		if ws.RootCount == 2 && !ws.HasGit && !ws.HasAnyGitRoot {
			hasWorkerDualNeither = true
		}
		if ws.RootCount == 2 && !ws.HasGit && ws.HasAnyGitRoot {
			hasWorkerDualSecondary = true
		}
		if ws.Loading == hoststate.WorkerLoadingPartlyLoaded {
			hasWorkerPartlyLoaded = true
		}
	}
	for _, app := range []string{"balanced", "light", "strict"} {
		if !workerApprovals[app] {
			t.Fatalf("WorkerStates missing approval posture %q in %v", app, workerApprovals)
		}
	}
	if !hasWorkerDualNeither || !hasWorkerDualSecondary || !hasWorkerPartlyLoaded {
		t.Fatalf("WorkerStates missing cases: dualNeither=%t dualSecondary=%t partlyLoaded=%t",
			hasWorkerDualNeither, hasWorkerDualSecondary, hasWorkerPartlyLoaded)
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
