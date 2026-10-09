package workflow

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowvalidation "github.com/lycaon/lycaon/internal/workflow/validation"
	"github.com/lycaon/lycaon/pkg/api"
)

// A phase whose gate only the host can settle is held while its ledger is
// pending; the hold ends the coordinator cycle.
func TestHostObligationHeldWhilePending(t *testing.T) {
	mgr, _, _, _ := testManager(t)
	obligation := &stubWorkflowObligation{status: api.WorkflowRunObligation{
		Kind: "scan", Status: api.ObligationStatusPending,
	}}
	wireTestObligation(t, mgr, obligation)
	mgr.Resolver.Overlay = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"obligationtest@1.0.0": obligationTestManifest()})

	ctx := context.Background()
	if _, err := startRun(ctx, mgr, "sess-1", "obligationtest", "1.0.0"); err != nil {
		testutil.FailErr(t, "start run", err)
	}
	held, err := mgr.Obligations.HostObligationHeld(ctx, "sess-1")
	testutil.FailErr(t, "HostObligationHeld", err)
	if !held {
		t.Fatal("a pending host obligation must hold the phase")
	}
	if kinds := mgr.Obligations.HostObligationHoldKinds(ctx, "sess-1"); len(kinds) != 1 || kinds[0] != "scan" {
		t.Fatalf("hold kinds = %#v want [scan]", kinds)
	}
}

// Every terminal status releases the hold, including the ones that settle the
// gate without producing results. A released hold lets the loop run again.
func TestHostObligationHeldReleasedByTerminalStatus(t *testing.T) {
	for _, status := range []string{
		api.ObligationStatusComplete,
		api.ObligationStatusFailed,
		api.ObligationStatusOff,
	} {
		t.Run(status, func(t *testing.T) {
			mgr, _, _, _ := testManager(t)
			obligation := &stubWorkflowObligation{status: api.WorkflowRunObligation{
				Kind: "scan", Status: api.ObligationStatusPending,
			}}
			wireTestObligation(t, mgr, obligation)
			mgr.Resolver.Overlay = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"obligationtest@1.0.0": obligationTestManifest()})

			ctx := context.Background()
			if _, err := startRun(ctx, mgr, "sess-1", "obligationtest", "1.0.0"); err != nil {
				testutil.FailErr(t, "start run", err)
			}
			obligation.status = api.WorkflowRunObligation{Kind: "scan", Status: status}
			held, err := mgr.Obligations.HostObligationHeld(ctx, "sess-1")
			testutil.FailErr(t, "HostObligationHeld", err)
			if held {
				t.Fatalf("status %q must release the hold", status)
			}
		})
	}
}

// A phase that declares no obligations is never held: ambient chat and ordinary
// work phases do not inherit the park.
func TestHostObligationHeldFalseWithoutDeclaredObligations(t *testing.T) {
	mgr, _, _, _ := testManagerWithRegistry(t)
	ctx := context.Background()
	if _, err := startRun(ctx, mgr, "sess-1", "plan", "1.0.0"); err != nil {
		testutil.FailErr(t, "start run", err)
	}
	held, err := mgr.Obligations.HostObligationHeld(ctx, "sess-1")
	testutil.FailErr(t, "HostObligationHeld", err)
	if held {
		t.Fatal("a phase without on_enter.obligations must not be held")
	}
}

func TestHostObligationHeldWhileTopologyPhaseIsRunning(t *testing.T) {
	mgr, _, _, _ := testManagerWithRegistry(t)
	ctx := context.Background()
	run, err := startRun(ctx, mgr, "sess-1", "bugbash", "1.0.0")
	testutil.FailErr(t, "start bugbash run", err)

	held, err := mgr.Obligations.HostObligationHeld(ctx, "sess-1")
	testutil.FailErr(t, "read hunt hold", err)
	if !held {
		t.Fatal("an incomplete topology-bound phase must hold the coordinator")
	}
	if kinds := mgr.Obligations.HostObligationHoldKinds(ctx, "sess-1"); len(kinds) != 1 || kinds[0] != topologyHostHoldKind {
		t.Fatalf("hold kinds = %#v want [%s]", kinds, topologyHostHoldKind)
	}

	for _, stage := range []string{"hunt_correctness", "hunt_edges", "hunt_races"} {
		testutil.FailErr(t, "complete hunt stage", mgr.Phases.MarkTopologyStageComplete(ctx, run.ID, stage, stage, ""))
	}
	held, err = mgr.Obligations.HostObligationHeld(ctx, "sess-1")
	testutil.FailErr(t, "read triage hold", err)
	if !held {
		t.Fatal("the next incomplete topology-bound phase must keep the coordinator held")
	}

	testutil.FailErr(t, "complete triage stage", mgr.Phases.MarkTopologyStageComplete(ctx, run.ID, "triage", "triaged", ""))
	held, err = mgr.Obligations.HostObligationHeld(ctx, "sess-1")
	testutil.FailErr(t, "read approval hold", err)
	if held {
		t.Fatal("the human approval phase is handled by the approval park, not a topology hold")
	}
}

// A session with no active run is not held — otherwise the loop would be
// silenced for every plain chat session.
func TestHostObligationHeldFalseWithoutActiveRun(t *testing.T) {
	mgr, _, _, _ := testManagerWithRegistry(t)
	held, err := mgr.Obligations.HostObligationHeld(context.Background(), "sess-none")
	testutil.FailErr(t, "HostObligationHeld", err)
	if held {
		t.Fatal("no active run must not be held")
	}
}

// The timerless park makes the settle the only wake, so a manifest whose
// post-obligation phase never prompts fails load rather than parking forever.
func TestValidateHostObligationWakeRejectsSilentSuccessor(t *testing.T) {
	m := workflowdef.FinalizeManifest(workflowdef.Manifest{
		ID:       "obligationtest",
		Version:  "1.0.0",
		Controls: workflowdef.ManifestControls{PhaseAdvance: workflowdef.PhaseAdvanceHost},
		PhaseDefs: []workflowdef.PhaseDef{
			{
				ID:           "ingest",
				CompleteWhen: workflowdef.CompleteWhenGatesSatisfied,
				Gates:        []string{workflowdef.ObligationGateLeaf("scan")},
				OnEnter:      workflowdef.PhaseOnEnter{Obligations: []workflowdef.ObligationDef{{Kind: "scan"}}},
				Next:         "work",
			},
			{ID: "work", CompleteWhen: workflowdef.CompleteWhenGatesSatisfied, Next: "done"},
			{ID: "done", Terminal: true, CompleteWhen: "orchestration_complete"},
		},
	})
	ingest, ok := m.PhaseByID("ingest")
	if !ok {
		t.Fatal("ingest phase missing")
	}
	errs := workflowvalidation.ValidateHostObligationWake(m, ingest)
	if len(errs) != 1 {
		t.Fatalf("want one diagnostic, got %#v", errs)
	}
	if errs[0].Code != "obligation_wake_unreachable" {
		t.Fatalf("code = %q", errs[0].Code)
	}
}

// A successor that prompts, is terminal, or chains its own host wait is a
// reachable wake and loads clean.
func TestValidateHostObligationWakeAcceptsReachableWakes(t *testing.T) {
	successors := map[string]workflowdef.PhaseDef{
		"prompts":  {ID: "next", CompleteWhen: workflowdef.CompleteWhenGatesSatisfied, OnEnter: workflowdef.PhaseOnEnter{PromptCoordinator: true}},
		"terminal": {ID: "next", Terminal: true, CompleteWhen: "orchestration_complete"},
		"chained": {
			ID:           "next",
			CompleteWhen: workflowdef.CompleteWhenGatesSatisfied,
			Gates:        []string{workflowdef.ObligationGateLeaf("scan")},
			OnEnter:      workflowdef.PhaseOnEnter{Obligations: []workflowdef.ObligationDef{{Kind: "scan"}}},
		},
	}
	for name, successor := range successors {
		t.Run(name, func(t *testing.T) {
			m := workflowdef.FinalizeManifest(workflowdef.Manifest{
				ID:       "obligationtest",
				Version:  "1.0.0",
				Controls: workflowdef.ManifestControls{PhaseAdvance: workflowdef.PhaseAdvanceHost},
				PhaseDefs: []workflowdef.PhaseDef{
					{
						ID:           "ingest",
						CompleteWhen: workflowdef.CompleteWhenGatesSatisfied,
						Gates:        []string{workflowdef.ObligationGateLeaf("scan")},
						OnEnter:      workflowdef.PhaseOnEnter{Obligations: []workflowdef.ObligationDef{{Kind: "scan"}}},
						Next:         "next",
					},
					successor,
				},
			})
			ingest, ok := m.PhaseByID("ingest")
			if !ok {
				t.Fatal("ingest phase missing")
			}
			if errs := workflowvalidation.ValidateHostObligationWake(m, ingest); len(errs) != 0 {
				t.Fatalf("want no diagnostics, got %#v", errs)
			}
		})
	}
}
