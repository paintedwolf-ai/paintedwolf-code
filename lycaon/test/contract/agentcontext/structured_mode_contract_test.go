package contract

import (
	"github.com/lycaon/lycaon/internal/toolcontract"

	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestTurnProfileActiveVsInactivePlan(t *testing.T) {
	t.Parallel()
	root := filepath.Join(contractcheck.RepoRoot(t), "lycaon")
	inactive := surface.ResolveTurnProfile(surface.EnrichRunContextForWorkflow(api.CoordinatorRunContext{}, root), &api.Session{Posture: api.SessionPostureBuild}, coordinatorTurnHistory(nil, "Hello"))
	active := surface.ResolveTurnProfile(surface.EnrichRunContextForWorkflow(api.CoordinatorRunContext{
		WorkflowID: "plan", WorkflowVersion: "1.0.0", SurfaceProfile: "plan",
		RunID: "run-1", CurrentPhase: "expand",
	}, root), &api.Session{Posture: api.SessionPostureSpec}, coordinatorTurnHistory(nil, surface.HostLoopWakeSentinel))

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
	root := contractcheck.RepoRoot(t)
	lycaonRoot := filepath.Join(root, "lycaon")
	inactive := renderCoordinatorTripartiteForRunContext(t, root, surface.EnrichRunContextForWorkflow(api.CoordinatorRunContext{}, lycaonRoot), &api.Session{Posture: api.SessionPostureBuild}, surface.HostLoopWakeSentinel, nil, "")
	active := renderCoordinatorTripartiteForRunContext(t, root, surface.EnrichRunContextForWorkflow(api.CoordinatorRunContext{
		WorkflowID: "plan", WorkflowVersion: "1.0.0", SurfaceProfile: "plan",
		RunID: "run-1", CurrentPhase: "expand",
	}, lycaonRoot), &api.Session{Posture: api.SessionPostureSpec}, surface.HostLoopWakeSentinel, nil, "")
	if inactive == active {
		t.Fatal("rendered policy text must differ for active vs inactive run")
	}
	if !strings.Contains(active, "Structured plan mode") {
		t.Fatal("active plan policy missing structured section")
	}
	if strings.Contains(inactive, "Structured plan mode — expand phase") {
		t.Fatal("implement-default policy must not include plan expand section")
	}
}
