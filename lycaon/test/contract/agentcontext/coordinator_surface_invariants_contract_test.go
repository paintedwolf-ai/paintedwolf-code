package contract

import (
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/progress"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

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
