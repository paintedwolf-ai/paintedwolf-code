package wiring

import (
	"github.com/lycaon/lycaon/internal/toolcontract"

	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/batch"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/pkg/api"
)

func inlineEditHistoryForWrapup() []api.Message {
	return []api.Message{
		{Role: api.MessageRoleUser, Content: "fix the handler"},
		{
			Role: api.MessageRoleAssistant,
			ToolCalls: []api.ToolCall{{ID: "c1", Name: "edit", Args: map[string]any{
				"path": "internal/handler.go",
			}}},
		},
		{
			Role:    api.MessageRoleTool,
			Content: "ok",
			ToolResult: &api.ToolResult{
				Outcome: api.ToolResultOutcomeCompleted,
				FileEdit: &api.FileEditSnapshot{
					Path: "internal/handler.go",
				},
			},
		},
	}
}

func TestSelectSurface_inlineEditNoVerifierRoutesInvestigate(t *testing.T) {
	history := inlineEditHistoryForWrapup()
	state := surface.ImplementSessionState{
		WorkersInFlight:           0,
		PendingOverlayIDs:         []string{},
		BatchPhase:                batch.PhaseSynthesize,
		WrapupGatesLoaded:         true,
		BatchReadyForSynthesis:    false,
		OpenRepairSinceUserIntent: false,
	}
	profile := surface.ResolveTurnProfile(
		api.CoordinatorRunContext{WorkflowID: "implement", CurrentPhase: "work"},
		&api.Session{Posture: api.SessionPostureBuild},
		routingTurnHistory(history, surface.HostLoopWakeSentinel),
		state,
	)
	if profile.SurfaceID != toolcontract.SurfaceImplementInvestigate {
		t.Fatalf("surface = %q want investigate when verify not passed", profile.SurfaceID)
	}
}

func TestSelectSurface_openRepairRoutesInvestigateDespiteSynthesizePhase(t *testing.T) {
	history := inlineEditHistoryForWrapup()
	state := surface.WithWrapupGates(surface.ImplementSessionState{
		WorkersInFlight:   0,
		PendingOverlayIDs: []string{},
		BatchPhase:        batch.PhaseSynthesize,
	}, false, true)
	profile := surface.ResolveTurnProfile(
		api.CoordinatorRunContext{WorkflowID: "implement", CurrentPhase: "work"},
		&api.Session{Posture: api.SessionPostureBuild},
		routingTurnHistory(history, surface.HostLoopWakeSentinel),
		state,
	)
	if profile.SurfaceID != toolcontract.SurfaceImplementInvestigate {
		t.Fatalf("surface = %q want investigate on open repair", profile.SurfaceID)
	}
}

func TestSelectSurface_manifestBoundSurfaceOverridesBatchReadySynthesis(t *testing.T) {
	state := surface.WithWrapupGates(surface.ImplementSessionState{
		WorkersInFlight:   0,
		PendingOverlayIDs: []string{},
		BatchPhase:        batch.PhaseSynthesize,
	}, true, false)
	profile := surface.ResolveTurnProfile(
		api.CoordinatorRunContext{
			WorkflowID:              "security-survey",
			CurrentPhase:            "challenge",
			PhaseCoordinatorSurface: "review_adjudicate",
		},
		&api.Session{Posture: api.SessionPostureVet},
		routingTurnHistory(completeImplementerHistoryForWrapup(), surface.HostLoopWakeSentinel),
		state,
	)
	if profile.SurfaceID != "review_adjudicate" {
		t.Fatalf("surface = %q want review_adjudicate when phase-bound despite batch ready", profile.SurfaceID)
	}
}

func TestSelectSurface_batchReadyAdmitsSynthesis(t *testing.T) {
	history := inlineEditHistoryForWrapup()
	state := surface.WithWrapupGates(surface.ImplementSessionState{
		WorkersInFlight:   0,
		PendingOverlayIDs: []string{},
		BatchPhase:        batch.PhaseSynthesize,
	}, true, false)
	profile := surface.ResolveTurnProfile(
		api.CoordinatorRunContext{WorkflowID: "implement", CurrentPhase: "work"},
		&api.Session{Posture: api.SessionPostureBuild},
		routingTurnHistory(history, surface.HostLoopWakeSentinel),
		state,
	)
	if profile.SurfaceID != "implement_synthesis" {
		t.Fatalf("surface = %q want implement_synthesis when batch ready", profile.SurfaceID)
	}
}

func TestSelectSurface_workersInFlightOverridesBatchReady(t *testing.T) {
	state := surface.WithWrapupGates(surface.ImplementSessionState{
		WorkersInFlight:   2,
		PendingOverlayIDs: []string{},
	}, true, false)
	profile := surface.ResolveTurnProfile(
		api.CoordinatorRunContext{},
		&api.Session{Posture: api.SessionPostureBuild},
		routingTurnHistory(completeImplementerHistoryForWrapup(), surface.HostLoopWakeSentinel),
		state,
	)
	if profile.SurfaceID != surface.SurfaceImplementPark {
		t.Fatalf("surface = %q want park when workers in flight", profile.SurfaceID)
	}
}

func completeImplementerHistoryForWrapup() []api.Message {
	return []api.Message{
		{Role: api.MessageRoleUser, Content: "dispatch fixes"},
		{
			Role:          api.MessageRoleAssistant,
			Content:       `<task job_id="j1" agent_type="` + orchestration.ProfileImplementer + `" state="complete"><summary>done</summary></task>`,
			WorkerSummary: &api.WorkerSummaryMeta{WorkerID: "j1", Status: "complete"},
		},
	}
}
