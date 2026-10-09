package surface

import (
	"github.com/lycaon/lycaon/internal/toolcontract"

	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestSelectSurfaceBatchPhasePreDispatchAllowsInvestigate(t *testing.T) {
	profile := resolvePickerTurnProfile(
		api.CoordinatorRunContext{WorkflowID: "implement", CurrentPhase: "work"},
		&api.Session{Posture: api.SessionPostureBuild},
		nil,
		"fix the auth bug",
		ImplementSessionState{BatchPhase: "pre_dispatch"},
	)
	if profile.SurfaceID != toolcontract.SurfaceImplementInvestigate {
		t.Fatalf("surface = %q want investigate in pre_dispatch", profile.SurfaceID)
	}
}

func TestSelectSurfaceBatchPhaseDispatchBlocksInvestigateOnHostTurn(t *testing.T) {
	profile := resolvePickerTurnProfile(
		api.CoordinatorRunContext{WorkflowID: "implement", CurrentPhase: "work"},
		&api.Session{Posture: api.SessionPostureBuild},
		nil,
		HostLoopWakeSentinel,
		ImplementSessionState{BatchPhase: "dispatch"},
	)
	if profile.SurfaceID != toolcontract.SurfaceImplementInvestigate {
		t.Fatalf("surface = %q want investigate on host turn without batch readiness", profile.SurfaceID)
	}
}

func TestSelectSurfaceVisibleUserTurnReopensInvestigateDuringBatchResidue(t *testing.T) {
	profile := resolvePickerTurnProfile(
		api.CoordinatorRunContext{WorkflowID: "implement", CurrentPhase: "work"},
		&api.Session{Posture: api.SessionPostureBuild},
		nil,
		"fix the auth bug",
		ImplementSessionState{BatchPhase: "dispatch"},
	)
	if profile.SurfaceID != toolcontract.SurfaceImplementInvestigate {
		t.Fatalf("visible user turn surface = %q want investigate despite dispatch residue", profile.SurfaceID)
	}
}

func TestSelectSurfaceBatchPhaseSynthesizeUsesOrchestrate(t *testing.T) {
	history := completeImplementerHistoryForPicker()
	state := batchReadySessionState()
	state.BatchPhase = "synthesize"
	profile := resolvePickerTurnProfile(
		api.CoordinatorRunContext{WorkflowID: "implement", CurrentPhase: "work"},
		&api.Session{Posture: api.SessionPostureBuild},
		history,
		HostLoopWakeSentinel,
		state,
	)
	if profile.SurfaceID != "implement_synthesis" {
		t.Fatalf("surface = %q want implement_synthesis", profile.SurfaceID)
	}
}

func TestSelectSurfaceBatchPhaseIntegrateUsesOverlayPromote(t *testing.T) {
	profile := resolvePickerTurnProfile(
		api.CoordinatorRunContext{WorkflowID: "implement", CurrentPhase: "work"},
		&api.Session{Posture: api.SessionPostureBuild},
		nil,
		HostLoopWakeSentinel,
		ImplementSessionState{
			BatchPhase:        "integrate",
			PendingOverlayIDs: []string{"job-1"},
		},
	)
	if profile.SurfaceID != SurfaceImplementOverlayPromote {
		t.Fatalf("surface = %q want %q", profile.SurfaceID, SurfaceImplementOverlayPromote)
	}
}

func TestSelectSurfaceBatchPhaseClosedBlocksInvestigateOnLoopWake(t *testing.T) {
	profile := resolvePickerTurnProfile(
		api.CoordinatorRunContext{WorkflowID: "implement", CurrentPhase: "work"},
		&api.Session{Posture: api.SessionPostureBuild},
		nil,
		HostLoopWakeSentinel,
		ImplementSessionState{BatchPhase: "closed"},
	)
	if profile.SurfaceID != toolcontract.SurfaceImplementInvestigate {
		t.Fatalf("closed batch loop wake surface = %q want investigate without batch readiness", profile.SurfaceID)
	}
}
