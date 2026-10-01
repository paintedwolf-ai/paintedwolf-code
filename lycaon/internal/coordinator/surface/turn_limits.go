package surface

import (
	"strings"

	"github.com/lycaon/lycaon/internal/settings"
)

// EffectivePromptLoopIterations returns the per-turn tool iteration cap for a coordinator surface.
func EffectivePromptLoopIterations(lim settings.SessionLimits, surfaceID string) int {
	max := lim.MaxIterations
	if strings.TrimSpace(surfaceID) != SurfaceImplementOverlayPromote {
		return max
	}
	promote := settings.NormalizeSessionLimits(lim).OverlayPromoteMaxIterations
	if promote > max {
		return promote
	}
	return max
}
