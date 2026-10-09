package phases_test

import (
	"context"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowphases "github.com/lycaon/lycaon/internal/workflow/phases"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
)

// The starting prompt is still inside the phase-enter hook, so the hook reports
// a run start. The host-obligation wiring cancels only a turn left on a previous
// surface.
func TestPhaseEnterHookReportsRunStartWhileObligationHeld(t *testing.T) {
	mgr, _, _, _ := testManager(t)
	obligation := &stubWorkflowObligation{status: api.WorkflowRunObligation{
		Kind: "scan", Status: api.ObligationStatusPending,
	}}
	wireTestObligation(t, mgr, obligation)
	mgr.Resolver.Overlay = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"obligationtest@1.0.0": obligationTestManifest()})

	var phases []string
	var runStarts []bool
	mgr.Phases.PhaseEnterHook = func(_ context.Context, rc *workflowphases.RunContext, _ workflowdef.PhaseDef) {
		phases = append(phases, rc.Phase)
		runStarts = append(runStarts, rc.IsRunStart())
	}

	ctx := context.Background()
	if _, err := startRun(ctx, mgr, "sess-1", "obligationtest", "1.0.0"); err != nil {
		testutil.FailErr(t, "start run", err)
	}

	held, err := mgr.Obligations.HostObligationHeld(ctx, "sess-1")
	testutil.FailErr(t, "HostObligationHeld", err)
	if !held {
		t.Fatal("the first phase must hold on its pending scan obligation")
	}
	if len(phases) != 1 || phases[0] != "ingest" {
		t.Fatalf("phase enter hooks = %#v want one for ingest", phases)
	}
	if !runStarts[0] {
		t.Fatal("a run start must report IsRunStart so the starting turn survives the hook")
	}
}

// A phase entered from another phase is an advance: its turn runs on the
// previous surface and is the one the wiring may cancel.
func TestRunContextIsRunStartOnlyWithoutPreviousPhase(t *testing.T) {
	for _, tc := range []struct {
		name     string
		previous string
		want     bool
	}{
		{name: "start", previous: "", want: true},
		{name: "blank previous", previous: "   ", want: true},
		{name: "advance", previous: "ingest", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rc := &workflowphases.RunContext{SessionID: "sess-1", Phase: "plan", PreviousPhase: tc.previous}
			if got := rc.IsRunStart(); got != tc.want {
				t.Fatalf("IsRunStart(previous=%q) = %v want %v", tc.previous, got, tc.want)
			}
		})
	}
}
