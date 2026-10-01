package surface_test

import (
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"testing"

	"github.com/lycaon/lycaon/internal/settings"
)

func TestEffectivePromptLoopIterationsOverlayPromoteBoost(t *testing.T) {
	lim := settings.SessionLimits{MaxIterations: 10, OverlayPromoteMaxIterations: 30}
	got := surface.EffectivePromptLoopIterations(lim, surface.SurfaceImplementOverlayPromote)
	if got != 30 {
		t.Fatalf("branch promote iterations = %d want 30", got)
	}
}

func TestEffectivePromptLoopIterationsOtherSurfaceUsesBase(t *testing.T) {
	lim := settings.SessionLimits{MaxIterations: 10, OverlayPromoteMaxIterations: 30}
	got := surface.EffectivePromptLoopIterations(lim, "implement_synthesis")
	if got != 10 {
		t.Fatalf("synthesis iterations = %d want 10", got)
	}
}
