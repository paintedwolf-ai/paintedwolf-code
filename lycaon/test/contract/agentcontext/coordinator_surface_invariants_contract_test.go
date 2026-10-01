package contract

import (
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/progress"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestCoordinatorSurfaceInvariants_implementDelegationAlwaysPresent(t *testing.T) {
	t.Parallel()
	surfaces := loadImplementSurfaces(t)
	for _, id := range []string{surfaceImplementRouting, surfaceImplementDispatch} {
		tools := surfaces[id]
		if !contractcheck.ContainsString(tools, "task") {
			t.Fatalf("%s must include %q (ambient implement dispatch path)", id, "task")
		}
		if contractcheck.ContainsString(tools, "delegate_dispatch") {
			t.Fatalf("%s must not expose delegate_dispatch — ambient implement dispatch is task() only", id)
		}
	}
	if contractcheck.ContainsString(surfaces[surfaceImplementSynthesis], "task") {
		t.Fatal("implement_synthesis must not expose task — wrapup is read-only report-only")
	}
}

func TestCoordinatorSurfaceInvariants_reconReconcileChoosesWithoutDispatch(t *testing.T) {
	t.Parallel()
	surfaces := loadImplementSurfaces(t)
	wire := surfaces["recon_reconcile"]
	if !contractcheck.ContainsString(wire, "workflow_transition") {
		t.Fatal("recon_reconcile must expose workflow_transition for report/deepen choice")
	}
	for _, forbidden := range []string{"task", "fanout_plan", "workflow_advance"} {
		if contractcheck.ContainsString(wire, forbidden) {
			t.Fatalf("recon_reconcile must not expose %q", forbidden)
		}
	}
}

// Progress-gated surfaces expose update_progress.
func TestCoordinatorSurfaceInvariants_progressGatedSurfacesExposeUpdateProgress(t *testing.T) {
	t.Parallel()
	surfaces := loadImplementSurfaces(t)
	var violations []string
	for id, wire := range surfaces {
		gated := false
		for _, tool := range wire {
			if progress.IsProgressGatedTool(tool) {
				gated = true
				break
			}
		}
		if !gated {
			continue
		}
		if !contractcheck.ContainsString(wire, "update_progress") {
			violations = append(violations, id)
		}
	}
	sort.Strings(violations)
	if len(violations) > 0 {
		t.Fatalf("surfaces with progress-gated tools must expose update_progress:\n  %s", strings.Join(violations, "\n  "))
	}
}

// update_progress accompanies a gated tool on the floor or loadable, except
// on report surfaces, where it reconciles the checklist the batch leaves.
func TestCoordinatorSurfaceInvariants_progressGateHasAnInlineTarget(t *testing.T) {
	t.Parallel()
	surfaces := loadImplementSurfaces(t)
	var violations []string
	for id, wire := range surfaces {
		if !contractcheck.ContainsString(wire, "update_progress") {
			continue
		}
		if exit, err := surface.SurfaceExit(id); err == nil && exit == surface.ExitReport {
			continue
		}
		inline := false
		for _, tool := range wire {
			if progress.IsProgressGatedTool(tool) {
				inline = true
				break
			}
		}
		if inline {
			continue
		}
		// A loadable gated tool joins the turn when the request needs it or
		// the model asks; the checklist rule still has a target on the surface.
		plan, err := surface.CompileToolPlan(surface.TurnProfile{SurfaceID: id}, 1)
		if err != nil {
			continue
		}
		loadable := false
		for _, tool := range plan.DeferredNames() {
			if progress.IsProgressGatedTool(tool) {
				loadable = true
				break
			}
		}
		if !loadable {
			violations = append(violations, id+" (no progress-gated tool on the floor or loadable)")
		}
	}
	sort.Strings(violations)
	if len(violations) > 0 {
		t.Fatalf("surfaces offering update_progress must carry a progress-gated tool on the floor or loadable:\n  %s",
			strings.Join(violations, "\n  "))
	}
}

// Surface cards describe their callable roster.
func TestCoordinatorSurfaceInvariants_surfaceCardTracksItsRoster(t *testing.T) {
	t.Parallel()
	surfaces := loadImplementSurfaces(t)
	for id, wire := range surfaces {
		plan, err := surface.CompileToolPlan(surface.TurnProfile{SurfaceID: id}, 1)
		var deferred []string
		if err != nil {
			deferred = nil
		} else {
			deferred = plan.DeferredNames()
		}
		card := renderSurfaceCardFor(t, id, wire, deferred)
		// Only positive clauses claim capabilities.
		claim := ""
		if start := strings.Index(card, "You may "); start >= 0 {
			if end := strings.Index(card[start:], " inline."); end >= 0 {
				claim = card[start : start+end]
			}
		}
		for _, group := range []struct {
			verb  string
			tools []string
		}{
			{"edit", []string{"write", "edit", "replace_lines", "delete", "code_rewrite"}},
			{"run commands", []string{"command", "verify"}},
			{"dispatch", []string{"task", "delegate_dispatch"}},
		} {
			if !strings.Contains(claim, group.verb) {
				continue
			}
			sticky := false
			for _, tool := range group.tools {
				if contractcheck.ContainsString(wire, tool) {
					sticky = true
					break
				}
			}
			if !sticky {
				t.Fatalf("%s card claims %q with no sticky member of %v: %q", id, group.verb, group.tools, card)
			}
		}
	}
}

func TestCoordinatorSurfaceInvariants_listDirOnImplementReadSurfaces(t *testing.T) {
	t.Parallel()
	surfaces := loadImplementSurfaces(t)
	for _, id := range []string{
		surfaceImplementRouting,
		surfaceImplementSynthesis,
		surfaceImplementOverlayPromote,
		"implement_investigate",
	} {
		if !contractcheck.ContainsString(surfaces[id], "list_dir") {
			t.Fatalf("%s must include list_dir (read-only symbol survey)", id)
		}
	}
	for _, id := range []string{surfaceImplementDispatch, surfaceImplementPark} {
		if contractcheck.ContainsString(surfaces[id], "list_dir") {
			t.Fatalf("%s must not include list_dir — dispatch/park surfaces are orchestration-only", id)
		}
	}
}

func TestCoordinatorSurfaceInvariants_implementSurfacesSubsetOfCoordinatorProfile(t *testing.T) {
	t.Parallel()
	profile := loadCoordinatorProfileTools(t)
	surfaces := loadImplementSurfaces(t)
	// Every surface tool fits within the coordinator profile.
	for _, id := range []string{surfaceImplementRouting, surfaceImplementDispatch, surfaceImplementSynthesis, surfaceImplementOverlayPromote, surfaceImplementPark, "implement_investigate"} {
		for _, tool := range surfaces[id] {
			if !coordinatorProfileGrantsTool(profile, tool) {
				t.Fatalf("%s lists %q which is not enabled on coordinator profile", id, tool)
			}
		}
	}
}
