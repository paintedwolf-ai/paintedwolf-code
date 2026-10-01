package workflow

import (
	"context"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

type workflowScanLedger struct {
	status api.CodeScanStatus
}

func (s *workflowScanLedger) ListByWorkflowRunID(context.Context, string) ([]api.CodeScan, error) {
	return []api.CodeScan{{
		ID: "scan-1", Status: s.status, FindingSetID: "findings-1",
		CoverageStatus: api.ScanCoverageComplete, FindingsCount: 1,
	}}, nil
}

func (s *workflowScanLedger) FullPassesForWorkflowRun(context.Context, string) ([]scan.FullPass, error) {
	return nil, nil
}

type workflowFindingHistory struct{}

func (workflowFindingHistory) IntroducedSince(context.Context, string, time.Time, api.FindingLevel) (int, error) {
	return 1, nil
}

func TestScanObligationUsesDeclaredGateInWorkflowStatus(t *testing.T) {
	for _, tc := range []struct {
		gate string
		want string
	}{
		{gate: "complete", want: api.ObligationStatusComplete},
		{gate: "no_new", want: api.ObligationStatusFailed},
	} {
		t.Run(tc.gate, func(t *testing.T) {
			mgr, _, _, projectDir := testManager(t)
			manifest := obligationTestManifest()
			manifest.PhaseDefs[0].OnEnter.Obligations[0].Params = map[string]any{
				"categories": []any{"security"}, "full": true, "gate": tc.gate,
			}
			mgr.Manifests = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"obligationtest@1.0.0": manifest})
			wireTestObligation(t, mgr, &stubWorkflowObligation{status: api.WorkflowRunObligation{Status: api.ObligationStatusPending}})
			run, err := startRun(t.Context(), mgr, "sess-1", "obligationtest", "1.0.0")
			testutil.FailErr(t, "start workflow", err)

			ledger := &workflowScanLedger{status: api.CodeScanStatusRunning}
			obligation := &scan.WorkflowObligation{
				Ledger: ledger, Params: mgr.ObligationParams, Runs: mgr.Get,
				Projects: func(context.Context, string) (string, error) { return projectDir, nil },
				History:  workflowFindingHistory{},
			}
			mgr.RegisterObligationKind(obligation)
			deps := conditions.TestRegistryDeps()
			deps.ObligationResolvers = map[string]conditions.ObligationStatusReader{"scan": obligation}
			reg, err := conditions.NewDefaultRegistry(deps)
			testutil.FailErr(t, "conditions registry", err)
			mgr.SetConditionRegistry(reg)

			held, err := mgr.HostObligationHeld(t.Context(), run.SessionID)
			testutil.FailErr(t, "read pending host hold", err)
			if !held {
				t.Fatal("full scan must hold the phase while running")
			}
			ledger.status = api.CodeScanStatusComplete
			status := mgr.PhaseObligationsUI(t.Context(), run, manifest.PhaseDefs[0])
			if len(status) != 1 || status[0].Status != tc.want {
				t.Fatalf("phase obligations = %#v, want %s", status, tc.want)
			}
			testutil.FailErr(t, "record terminal scan", mgr.RecordObligationTerminal(t.Context(), run.ID, "scan"))
			vars, err := mgr.Store.GetScaffoldVars(t.Context(), run.ID)
			testutil.FailErr(t, "read obligation stamp", err)
			summary, _ := ObligationsFromVars(vars)["scan"].(map[string]any)
			if summary["status"] != tc.want {
				t.Fatalf("stamped scan obligation = %#v, want %s", summary, tc.want)
			}
		})
	}
}

// A scan that settles after the run left the phase that bound it reads that
// phase's terms; the run's current phase declares no scan obligation.
func TestScanObligationStatusNamesThePhaseItWasAskedAbout(t *testing.T) {
	mgr, _, _, projectDir := testManager(t)
	manifest := obligationTestManifest()
	manifest.PhaseDefs[0].OnEnter.Obligations[0].Params = map[string]any{"categories": []any{"security"}, "full": true}
	mgr.Manifests = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"obligationtest@1.0.0": manifest})
	wireTestObligation(t, mgr, &stubWorkflowObligation{status: api.WorkflowRunObligation{Status: api.ObligationStatusPending}})
	run, err := startRun(t.Context(), mgr, "sess-1", "obligationtest", "1.0.0")
	testutil.FailErr(t, "start workflow", err)
	bound := manifest.PhaseDefs[0].ID
	if len(manifest.PhaseDefs) < 2 {
		t.Fatal("the obligation test manifest needs a phase after the bound one")
	}
	run.CurrentPhase = manifest.PhaseDefs[1].ID
	testutil.FailErr(t, "leave the bound phase", mgr.Store.Update(t.Context(), run))

	obligation := &scan.WorkflowObligation{
		Ledger: &workflowScanLedger{status: api.CodeScanStatusComplete}, Params: mgr.ObligationParams, Runs: mgr.Get,
		Projects: func(context.Context, string) (string, error) { return projectDir, nil },
		History:  workflowFindingHistory{},
	}
	status, err := obligation.Status(t.Context(), run.ID, bound)
	testutil.FailErr(t, "status for the phase the run left", err)
	if status.Status != api.ObligationStatusComplete {
		t.Fatalf("status = %#v, want the bound phase's full scan complete", status)
	}
	if _, err := mgr.ObligationParams(t.Context(), run.ID, manifest.PhaseDefs[1].ID, "scan"); err == nil {
		t.Fatal("a phase that declares no scan obligation returned parameters")
	}
}
