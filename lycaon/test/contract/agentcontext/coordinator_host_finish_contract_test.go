package contract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/pkg/api"
)

// hostProseFinishContract classifies every coordinator surface in coordinator-surfaces.yaml
// by whether a host cycle turn may close it with user-facing prose once the roster is idle
// and nothing is pending. `true` = the surface reports findings to the user (routing /
// synthesis); `false` = it terminates via a tool (dispatch/promote/wait, or a plan/compose
// surface with its own gating). Every surface in the YAML needs an entry.
var hostProseFinishContract = map[string]bool{
	"implement_routing":         true,
	"implement_synthesis":       true,
	"implement_dispatch":        false,
	"implement_overlay_promote": false,
	"implement_park":            false,
	"implement_investigate":     true,
	"observe_investigate":       true,
	"observe_synthesis":         true,
	"observe_plan":              false,
	"workflow_compose":          false,
	"plan_stub":                 false,
	"plan_research":             false,
	"plan_review":               false,
	"survey_execute":            false,
	"review_adjudicate":         false,
	"decision_adjudicate":       false,
	"plan_approve":              false,
	"await_user":                false,
	"await_host":                false,
	"plan_execute":              false,
	"orchestrate_plan":          false,
	"recon_reconcile":           false,
}

// TestCoordinatorHostFinish_surfaceClassificationExhaustive: every configured surface is
// classified, every classified surface exists in coordinator-surfaces.yaml, and the
// classification matches HostTurnMayFinishWithProse.
func TestCoordinatorHostFinish_surfaceClassificationExhaustive(t *testing.T) {
	t.Parallel()
	surfaces := loadImplementSurfaces(t)
	for id := range surfaces {
		want, ok := hostProseFinishContract[id]
		if !ok {
			t.Fatalf("surface %q is unclassified — add it to hostProseFinishContract (true if a host cycle turn may close it with user prose once idle, false if it requires a tool terminal)", id)
		}
		if got := guard.HostTurnMayFinishWithProse(id, true, false); got != want {
			t.Fatalf("surface %q: HostTurnMayFinishWithProse(idle, nothing pending) = %v, want %v", id, got, want)
		}
	}
	for id := range hostProseFinishContract {
		if _, ok := surfaces[id]; !ok {
			t.Fatalf("hostProseFinishContract lists %q which is absent from coordinator-surfaces.yaml", id)
		}
	}
}

// A read-only scout fan-out (path explorers, repo researchers) resolves to
// synthesis on its idle loop wake, and that turn must be able to close with a
// grounded user answer, or the coordinator livelocks re-emitting its summary.
func TestCoordinatorHostFinish_readScoutsCanReportToUser(t *testing.T) {
	t.Parallel()
	for _, scout := range []string{orchestration.ProfilePathExplorer, orchestration.ProfileRepoResearcher} {
		t.Run(scout, func(t *testing.T) {
			t.Parallel()
			profile := surface.ResolveTurnProfile(
				api.CoordinatorRunContext{},
				&api.Session{Posture: api.SessionPostureBuild},
				coordinatorTurnHistory(WorkerCompletionHistory(scout), surface.HostLoopWakeSentinel),
				surface.WithWrapupGates(surface.ImplementSessionState{}, true, false),
			)
			if profile.SurfaceID != "implement_synthesis" {
				t.Fatalf("%s loop wake resolved to surface %q, want implement_synthesis", scout, profile.SurfaceID)
			}
			if !guard.HostTurnMayFinishWithProse(profile.SurfaceID, true, false) {
				t.Fatalf("%s synthesis on %q cannot finish with a grounded user answer", scout, profile.SurfaceID)
			}
		})
	}
}
