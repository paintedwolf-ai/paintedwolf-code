package contract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/pkg/api"
)

func userTurn() []api.Message {
	return []api.Message{{Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Visibility: api.MessageVisibilityTranscript, Content: "fix auth in src/"}}
}

func hostWakeTurn() []api.Message {
	return []api.Message{{Role: api.MessageRoleUser, Origin: api.MessageOriginHost, Visibility: api.MessageVisibilityInternal, Kind: api.MessageKindHostLoopWake, Content: surface.HostLoopWakeSentinel}}
}

func workerFinishedTurn() []api.Message {
	return []api.Message{{Role: api.MessageRoleUser, Origin: api.MessageOriginHost, Visibility: api.MessageVisibilityInternal, Kind: api.MessageKindHostKick, HostSignalID: anchor.WorkerTaskFinished.String()}}
}

// Each case names the surface and base mode the shipped flow table must choose;
// the expected values are written from the table's rules, not computed.
func TestCoordinatorFlowParity_ResolveTurnProfileMatchesExpectedSurface(t *testing.T) {
	t.Parallel()
	build := &api.Session{Posture: api.SessionPostureBuild}
	spec := &api.Session{Posture: api.SessionPostureSpec}
	vet := &api.Session{Posture: api.SessionPostureVet}
	notEligible := false
	orchestrate := api.CoordinatorRunContext{WorkflowDefaultExecutionMode: surface.ExecutionModeFamilyOrchestrate}
	investigate := api.CoordinatorRunContext{WorkflowDefaultExecutionMode: surface.ExecutionModeFamilyInvestigate}
	ineligible := api.CoordinatorRunContext{WorkflowInvestigateEligible: &notEligible}
	batchReady := surface.WithWrapupGates(surface.ImplementSessionState{}, true, false)
	overlay := surface.ImplementSessionState{PendingOverlayIDs: []string{"overlay-1"}}
	workersOut := surface.ImplementSessionState{WorkersInFlight: 1}
	for _, tc := range []struct {
		name     string
		runCtx   api.CoordinatorRunContext
		sess     *api.Session
		history  []api.Message
		state    surface.ImplementSessionState
		wantID   string
		wantMode string
	}{
		{"compose draft", api.CoordinatorRunContext{HasComposeDraft: true}, build, userTurn(), surface.ImplementSessionState{}, "workflow_compose", "compose"},
		{"overlay pending", api.CoordinatorRunContext{}, build, hostWakeTurn(), overlay, "implement_overlay_promote", "implement-overlay-promote"},
		{"overlay pending in vet", api.CoordinatorRunContext{}, vet, hostWakeTurn(), overlay, "observe_investigate", "observe-investigate"},
		{"worker delivered", api.CoordinatorRunContext{}, build, hostWakeTurn(), surface.ImplementSessionState{WorkersDelivered: 1}, "implement_overlay_promote", "implement-overlay-promote"},
		{"host-held phase answers the person", api.CoordinatorRunContext{PhaseHostHeld: true, PhaseCoordinatorSurface: "plan_research"}, build, userTurn(), surface.ImplementSessionState{}, "await_host", "await-host"},
		{"manifest-bound plan research", api.CoordinatorRunContext{PhaseCoordinatorSurface: "plan_research"}, spec, nil, surface.ImplementSessionState{}, "plan_research", "plan-research"},
		{"manifest-bound plan execute", api.CoordinatorRunContext{PhaseCoordinatorSurface: "plan_execute"}, spec, nil, surface.ImplementSessionState{}, "plan_execute", "plan-execute"},
		{"phase mode refs set the base mode", api.CoordinatorRunContext{PhaseCoordinatorSurface: "plan_research", PhaseModeRefs: []string{"plan-execute"}}, spec, nil, surface.ImplementSessionState{}, "plan_research", "plan-execute"},
		{"paused on a child workflow", api.CoordinatorRunContext{RunStatus: string(api.WorkflowRunStatusPausedOnChild)}, build, userTurn(), surface.ImplementSessionState{}, "implement_dispatch", "implement-dispatch"},
		{"orchestrate before dispatch", orchestrate, build, userTurn(), surface.ImplementSessionState{}, "implement_dispatch", "implement-dispatch"},
		{"orchestrate with workers out", orchestrate, build, hostWakeTurn(), workersOut, "implement_park", "implement-park"},
		{"declared investigate", investigate, build, userTurn(), surface.ImplementSessionState{}, "implement_investigate", "implement-investigate"},
		{"declared investigate in vet", investigate, vet, userTurn(), surface.ImplementSessionState{}, "observe_investigate", "observe-investigate"},
		{"no workflow defaults to investigate", api.CoordinatorRunContext{}, build, userTurn(), surface.ImplementSessionState{}, "implement_investigate", "implement-investigate"},
		{"worker finished with workers out", ineligible, build, workerFinishedTurn(), workersOut, "implement_dispatch", "implement-dispatch"},
		{"host wake with workers out", api.CoordinatorRunContext{}, build, hostWakeTurn(), workersOut, "implement_park", "implement-park"},
		{"batch ready for synthesis", ineligible, build, hostWakeTurn(), batchReady, "implement_synthesis", "implement-synthesis"},
		{"batch ready for synthesis in vet", ineligible, vet, hostWakeTurn(), batchReady, "observe_synthesis", "implement-synthesis"},
		{"host cycle with open progress under orchestrate", orchestrate, build, hostWakeTurn(),
			surface.ImplementSessionState{BatchReadyForSynthesis: true, ProgressOpenCount: 1}, "implement_dispatch", "implement-dispatch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			profile := surface.ResolveTurnProfile(tc.runCtx, tc.sess, tc.history, tc.state)
			if profile.SurfaceID != tc.wantID {
				t.Fatalf("surface = %q, want %q", profile.SurfaceID, tc.wantID)
			}
			if len(profile.ModeRefs) == 0 || profile.ModeRefs[0] != tc.wantMode {
				t.Fatalf("mode refs = %v, want base %q", profile.ModeRefs, tc.wantMode)
			}
		})
	}
}
