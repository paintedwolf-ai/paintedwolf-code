package workflow

import (
	"context"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
)

type stubWorkflowObligation struct {
	status   api.WorkflowRunObligation
	enterErr error
	onEnter  func(*api.WorkflowRun)
}

func (s *stubWorkflowObligation) Kind() string { return "scan" }

func (s *stubWorkflowObligation) ValidateParams(map[string]any) error { return nil }

func (s *stubWorkflowObligation) OnPhaseEnter(_ context.Context, run *api.WorkflowRun, _ string, _ map[string]any) error {
	if s.onEnter != nil {
		s.onEnter(run)
	}
	return s.enterErr
}

func (s *stubWorkflowObligation) Status(context.Context, string, string) (api.WorkflowRunObligation, error) {
	return s.status, nil
}

func obligationTestManifest() workflowdef.Manifest {
	return workflowdef.FinalizeManifest(workflowdef.Manifest{
		ID:       "obligationtest",
		Version:  "1.0.0",
		Controls: workflowdef.ManifestControls{PhaseAdvance: workflowdef.PhaseAdvanceHost},
		PhaseDefs: []workflowdef.PhaseDef{
			{
				ID:           "ingest",
				CompleteWhen: workflowdef.CompleteWhenGatesSatisfied,
				Gates:        []string{workflowdef.ObligationGateLeaf("scan")},
				OnEnter: workflowdef.PhaseOnEnter{Obligations: []workflowdef.ObligationDef{{
					Kind: "scan", Params: map[string]any{"categories": []any{"security"}},
				}}},
				Next: "done",
			},
			{ID: "done", Terminal: true, CompleteWhen: "orchestration_complete"},
		},
	})
}

func wireTestObligation(t *testing.T, mgr *RunManager, obligation *stubWorkflowObligation) {
	t.Helper()
	mgr.Obligations.Register(obligation)
	deps := conditions.TestRegistryDeps()
	deps.ObligationResolvers = map[string]conditions.ObligationStatusReader{"scan": obligation}
	reg, err := conditions.NewDefaultRegistry(deps)
	testutil.FailErr(t, "conditions registry", err)
	mgr.SetConditionRegistry(reg)
}

func TestRecordObligationTerminalAdvancesPhase(t *testing.T) {
	mgr, _, _, _ := testManager(t)
	obligation := &stubWorkflowObligation{status: api.WorkflowRunObligation{
		Kind: "scan", Status: api.ObligationStatusPending,
	}}
	wireTestObligation(t, mgr, obligation)
	mgr.Resolver.Overlay = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"obligationtest@1.0.0": obligationTestManifest()})

	ctx := context.Background()
	run, err := startRun(ctx, mgr, "sess-1", "obligationtest", "1.0.0")
	testutil.FailErr(t, "start run", err)
	state := mgr.Policy.ActivePhaseGuardState(ctx, "sess-1")
	if !state.PhaseObligationPending || len(state.PendingObligationKinds) != 1 || state.PendingObligationKinds[0] != "scan" {
		t.Fatalf("phase guard state = %#v", state)
	}

	obligation.status = api.WorkflowRunObligation{
		Kind: "scan", Status: api.ObligationStatusComplete, Detail: map[string]any{"findings_count": 4},
	}
	testutil.FailErr(t, "record terminal obligation", mgr.Obligations.RecordObligationTerminal(ctx, run.ID, "scan"))

	after, err := mgr.Store.Runs.Get(ctx, run.ID)
	testutil.FailErr(t, "get run", err)
	if after.CurrentPhase != "done" {
		t.Fatalf("phase = %q, want done", after.CurrentPhase)
	}
	vars, err := mgr.Store.Runs.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "get vars", err)
	scanSummary, _ := ObligationsFromVars(vars)["scan"].(map[string]any)
	// Scaffold vars round-trip through JSON, so numbers come back as float64.
	if scanSummary["status"] != api.ObligationStatusComplete || fmt.Sprintf("%v", scanSummary["findings_count"]) != "4" {
		t.Fatalf("scan obligation = %#v", scanSummary)
	}
}

func TestObligationEnqueueFailureSettlesPhase(t *testing.T) {
	mgr, _, _, _ := testManager(t)
	obligation := &stubWorkflowObligation{
		status:   api.WorkflowRunObligation{Kind: "scan", Status: api.ObligationStatusEmpty},
		enterErr: errors.New("snapshot unavailable"),
	}
	wireTestObligation(t, mgr, obligation)
	mgr.Resolver.Overlay = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"obligationtest@1.0.0": obligationTestManifest()})

	ctx := context.Background()
	run, err := startRun(ctx, mgr, "sess-1", "obligationtest", "1.0.0")
	testutil.FailErr(t, "start run", err)
	vars, err := mgr.Store.Runs.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "get vars", err)
	scanSummary, _ := ObligationsFromVars(vars)["scan"].(map[string]any)
	if scanSummary["status"] != api.ObligationStatusFailed || scanSummary["error"] != "snapshot unavailable" {
		t.Fatalf("scan obligation = %#v", scanSummary)
	}
}

func TestAdvanceConvergesAcrossSettledObligation(t *testing.T) {
	mgr, _, _, projectDir := testManager(t)
	obligation := &stubWorkflowObligation{status: api.WorkflowRunObligation{
		Kind: "scan", Status: api.ObligationStatusPending,
	}}
	obligation.onEnter = func(*api.WorkflowRun) {
		obligation.status.Status = api.ObligationStatusComplete
	}
	wireTestObligation(t, mgr, obligation)
	manifest := workflowdef.FinalizeManifest(workflowdef.Manifest{
		ID:       "convergeobligation",
		Version:  "1.0.0",
		Controls: workflowdef.ManifestControls{PhaseAdvance: workflowdef.PhaseAdvanceHost},
		PhaseDefs: []workflowdef.PhaseDef{
			{
				ID:                 "plan",
				CompleteWhen:       workflowdef.CompleteWhenGatesSatisfied,
				Gates:              []string{"fanout_planned"},
				AdvanceWhenGateMet: workflowdef.AdvanceWhenGateMetCoordinator,
				Next:               "ingest",
			},
			{
				ID:           "ingest",
				CompleteWhen: workflowdef.CompleteWhenGatesSatisfied,
				Gates:        []string{workflowdef.ObligationGateLeaf("scan")},
				OnEnter: workflowdef.PhaseOnEnter{Obligations: []workflowdef.ObligationDef{{
					Kind: "scan", Params: map[string]any{"categories": []any{"security"}},
				}}},
				Next: "execute",
			},
			{
				ID: "execute", CompleteWhen: workflowdef.CompleteWhenGatesSatisfied,
				Gates: []string{"worker_cycle_ready"}, Next: "done",
			},
			{ID: "done", Terminal: true, CompleteWhen: "orchestration_complete"},
		},
	})
	mgr.Resolver.Overlay = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"convergeobligation@1.0.0": manifest})

	ctx := context.Background()
	run, err := startRun(ctx, mgr, "sess-1", "convergeobligation", "1.0.0")
	testutil.FailErr(t, "start run", err)
	vars, err := mgr.Store.Runs.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "get vars", err)
	vars = runstate.SatisfyGateInVars(vars, "fanout_planned")
	testutil.FailErr(t, "update vars", mgr.Store.State.UpdateVars(ctx, run, projectDir, vars))

	advanced, err := mgr.Phases.Advance(ctx, run.ID)
	testutil.FailErr(t, "advance", err)
	if advanced.CurrentPhase != "execute" {
		t.Fatalf("phase = %q, want execute", advanced.CurrentPhase)
	}
}
