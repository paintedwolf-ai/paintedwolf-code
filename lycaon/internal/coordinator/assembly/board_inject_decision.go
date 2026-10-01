package assembly

import (
	"github.com/lycaon/lycaon/internal/packboard"
)

type boardInjectDecision struct {
	Inject        bool
	Scope         packboard.InjectScope
	OrientationFP string
	PulseFP       string
}

func decideBoardInject(
	st boardInjectState,
	orientationFP, pulseFP, runID, phase string,
) boardInjectDecision {
	if !st.injectedOnce ||
		runID != st.lastWorkflowRunID ||
		(phase != "" && phase != st.lastWorkflowPhase) ||
		orientationFP != st.lastOrientationFP {
		return boardInjectDecision{
			Inject:        true,
			Scope:         packboard.InjectScopeFull,
			OrientationFP: orientationFP,
			PulseFP:       pulseFP,
		}
	}
	if pulseFP != st.lastPulseFP {
		return boardInjectDecision{
			Inject:        true,
			Scope:         packboard.InjectScopePulse,
			OrientationFP: orientationFP,
			PulseFP:       pulseFP,
		}
	}
	return boardInjectDecision{}
}

func (d boardInjectDecision) cacheKey(runID, phase string) string {
	if !d.Inject {
		return ""
	}
	return orientationPulseCacheKey(d.OrientationFP, d.PulseFP, runID, phase)
}

func orientationPulseCacheKey(orientationFP, pulseFP, runID, phase string) string {
	return orientationFP + "\x00" + pulseFP + "\x00" + runID + "\x00" + phase
}
