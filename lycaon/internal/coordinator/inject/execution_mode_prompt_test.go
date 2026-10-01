package inject

import (
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"testing"

	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestExecutionModeFamilyCoversOrchestrateSurfaces(t *testing.T) {
	t.Helper()
	for _, surfaceID := range []string{
		spawn.SurfaceImplementRouting,
		spawn.SurfaceImplementDispatch,
		surface.SurfaceImplementOverlayPromote,
		surface.SurfaceImplementPark,
	} {
		if got := surface.ExecutionModeFamily(surfaceID); got != surface.ExecutionModeFamilyOrchestrate {
			t.Fatalf("surface.ExecutionModeFamily(%q) = %q, want orchestrate", surfaceID, got)
		}
	}
	if got := surface.ExecutionModeFamily(spawn.SurfaceImplementSynthesis); got != surface.ExecutionModeFamilyWrapup {
		t.Fatalf("synthesis surface family = %q, want wrapup", got)
	}
	if got := surface.ExecutionModeFamily(tools.SurfaceImplementInvestigate); got != surface.ExecutionModeFamilyInvestigate {
		t.Fatalf("investigate surface family = %q", got)
	}
	if got := surface.ExecutionModeFamily("plan_stub"); got != "" {
		t.Fatalf("catalog surface family = %q, want empty", got)
	}
}

func TestComputeModeTransitionColdStart(t *testing.T) {
	got := surface.ComputeModeTransition("", surface.ExecutionModeFamilyInvestigate, nil)
	if got.ExecutionModeEntered != "" || got.ExecutionModeLeft != "" {
		t.Fatalf("cold start entered/left should be empty: %+v", got)
	}
	if got.ExecutionMode != surface.ExecutionModeFamilyInvestigate {
		t.Fatalf("execution_mode = %q", got.ExecutionMode)
	}
}

func TestComputeModeTransitionExplicitWorkflowDefault(t *testing.T) {
	got := surface.ComputeModeTransition("", surface.ExecutionModeFamilyOrchestrate, []surface.ModeTransitionCause{{
		Kind: surface.ModeTransitionCauseWorkflowDefault,
		Mode: surface.ExecutionModeFamilyOrchestrate,
	}})
	if got.ExecutionModeEntered != surface.ExecutionModeFamilyOrchestrate {
		t.Fatalf("entered = %q", got.ExecutionModeEntered)
	}
	if got.ExecutionModeLeft != "" {
		t.Fatalf("explicit entry suppresses left: %+v", got)
	}
}

func TestComputeModeTransitionActionDriven(t *testing.T) {
	got := surface.ComputeModeTransition(surface.ExecutionModeFamilyInvestigate, surface.ExecutionModeFamilyOrchestrate, nil)
	if got.ExecutionModeEntered != surface.ExecutionModeFamilyOrchestrate {
		t.Fatalf("entered = %q", got.ExecutionModeEntered)
	}
	if got.ExecutionModeLeft != surface.ExecutionModeFamilyInvestigate {
		t.Fatalf("left = %q", got.ExecutionModeLeft)
	}
}

func TestComputeModeTransitionSteady(t *testing.T) {
	got := surface.ComputeModeTransition(surface.ExecutionModeFamilyOrchestrate, surface.ExecutionModeFamilyOrchestrate, nil)
	if got.ExecutionModeEntered != "" || got.ExecutionModeLeft != "" {
		t.Fatalf("steady turn should not enter/leave: %+v", got)
	}
}

func TestComputeModeTransitionAtMostOneCause(t *testing.T) {
	causes := []surface.ModeTransitionCause{
		{Kind: surface.ModeTransitionCauseWorkflowDefault, Mode: surface.ExecutionModeFamilyInvestigate},
		{Kind: surface.ModeTransitionCausePhaseHook, Mode: surface.ExecutionModeFamilyOrchestrate},
	}
	got := surface.ComputeModeTransition("", surface.ExecutionModeFamilyInvestigate, causes)
	if got.ExecutionModeEntered != surface.ExecutionModeFamilyInvestigate {
		t.Fatalf("first cause wins: entered=%q", got.ExecutionModeEntered)
	}
}
