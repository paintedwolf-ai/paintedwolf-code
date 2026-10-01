package assembly

import (
	"testing"

	"github.com/lycaon/lycaon/internal/packboard"
)

func TestDecideBoardInjectFullOnFirstTurn(t *testing.T) {
	st := boardInjectState{}
	decision := decideBoardInject(st, "orient-a", "pulse-a", "", "implement")
	if !decision.Inject || decision.Scope != packboard.InjectScopeFull {
		t.Fatalf("decision = %+v", decision)
	}
}

func TestDecideBoardInjectNoneWhenStable(t *testing.T) {
	st := boardInjectState{
		injectedOnce:      true,
		lastOrientationFP: "orient-a",
		lastPulseFP:       "pulse-a",
		lastWorkflowPhase: "implement",
	}
	decision := decideBoardInject(st, "orient-a", "pulse-a", "", "implement")
	if decision.Inject {
		t.Fatalf("decision = %+v", decision)
	}
}

func TestDecideBoardInjectPulseOnWorkerChange(t *testing.T) {
	st := boardInjectState{
		injectedOnce:      true,
		lastOrientationFP: "orient-a",
		lastPulseFP:       "pulse-a",
		lastWorkflowPhase: "implement",
	}
	decision := decideBoardInject(st, "orient-a", "pulse-b", "", "implement")
	if !decision.Inject || decision.Scope != packboard.InjectScopePulse {
		t.Fatalf("decision = %+v", decision)
	}
}

func TestDecideBoardInjectFullOnOrientationChange(t *testing.T) {
	st := boardInjectState{
		injectedOnce:      true,
		lastOrientationFP: "orient-a",
		lastPulseFP:       "pulse-a",
		lastWorkflowPhase: "implement",
	}
	decision := decideBoardInject(st, "orient-b", "pulse-a", "", "implement")
	if !decision.Inject || decision.Scope != packboard.InjectScopeFull {
		t.Fatalf("decision = %+v", decision)
	}
}

func TestDecideBoardInjectFullOnPhaseChange(t *testing.T) {
	st := boardInjectState{
		injectedOnce:      true,
		lastOrientationFP: "orient-a",
		lastPulseFP:       "pulse-a",
		lastWorkflowPhase: "plan",
	}
	decision := decideBoardInject(st, "orient-a", "pulse-a", "", "implement")
	if !decision.Inject || decision.Scope != packboard.InjectScopeFull {
		t.Fatalf("decision = %+v", decision)
	}
}
