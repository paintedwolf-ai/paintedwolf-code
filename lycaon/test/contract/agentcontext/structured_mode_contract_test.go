package contract

import (
	"github.com/lycaon/lycaon/internal/toolcontract"

	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestTurnProfileActiveVsInactivePlan(t *testing.T) {
	t.Parallel()
	inactive := surface.ResolveTurnProfile(bindFixturePhase(t, api.CoordinatorRunContext{}), &api.Session{Posture: api.SessionPostureBuild}, coordinatorTurnHistory(nil, "Hello"))
	active := surface.ResolveTurnProfile(bindFixturePhase(t, api.CoordinatorRunContext{
		WorkflowID: "plan", WorkflowVersion: "1.0.0", SurfaceProfile: "plan",
		RunID: "run-1", CurrentPhase: "expand",
	}), &api.Session{Posture: api.SessionPostureSpec}, coordinatorTurnHistory(nil, surface.HostLoopWakeSentinel))

	if inactive.SurfaceID == active.SurfaceID && strings.Join(inactive.ModeRefs, "+") == strings.Join(active.ModeRefs, "+") {
		t.Fatalf("profiles must differ: inactive=%+v active=%+v", inactive, active)
	}
	if active.SurfaceID != "plan_stub" {
		t.Fatalf("active surface = %q want plan_stub", active.SurfaceID)
	}
	if inactive.SurfaceID != toolcontract.SurfaceImplementInvestigate {
		t.Fatalf("inactive surface = %q want implement_investigate", inactive.SurfaceID)
	}
}

func TestCoordinatorTripartiteRenderDiffersActiveVsInactive(t *testing.T) {
	t.Parallel()
	inactive := renderCoordinatorTripartiteForRunContext(t, bindFixturePhase(t, api.CoordinatorRunContext{}), &api.Session{Posture: api.SessionPostureBuild}, surface.HostLoopWakeSentinel, nil, "")
	active := renderCoordinatorTripartiteForRunContext(t, bindFixturePhase(t, api.CoordinatorRunContext{
		WorkflowID: "plan", WorkflowVersion: "1.0.0", SurfaceProfile: "plan",
		RunID: "run-1", CurrentPhase: "expand",
	}), &api.Session{Posture: api.SessionPostureSpec}, surface.HostLoopWakeSentinel, nil, "")
	if inactive == active {
		t.Fatal("rendered policy text must differ for active vs inactive run")
	}
}
