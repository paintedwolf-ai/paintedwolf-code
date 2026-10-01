//go:build integration

package workflow

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestReconProgressiveScoutCanReportOrDrill(t *testing.T) {
	for _, tc := range []struct {
		name       string
		transition string
		wantPhase  string
	}{
		{name: "report after scout", transition: "report", wantPhase: "report"},
		{name: "deepen after scout", transition: "deepen", wantPhase: "drill_plan"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mgr, _, _, projectDir := testManagerWithRegistry(t)
			ctx := context.Background()
			run, err := startRun(ctx, mgr, "sess-1", "recon-pack", "1.0.0")
			testutil.FailErr(t, "start recon", err)
			if run.CurrentPhase != "plan" {
				t.Fatalf("start phase = %q want plan", run.CurrentPhase)
			}

			run = stampReconWave(t, ctx, mgr, run, projectDir, "Scout the user question")
			run, err = mgr.Advance(ctx, run.ID)
			testutil.FailErr(t, "advance plan", err)
			if run.CurrentPhase != "execute" {
				t.Fatalf("phase = %q want execute", run.CurrentPhase)
			}

			testutil.FailErr(t, "complete scout wave", mgr.RecordWorkerTerminalProof(ctx, run.SessionID, "scout-1", "complete"))
			run, err = mgr.Get(ctx, run.ID)
			testutil.FailErr(t, "get after scout", err)
			if run.CurrentPhase != "reconcile" {
				t.Fatalf("phase = %q want reconcile", run.CurrentPhase)
			}

			run, err = mgr.FireTransition(ctx, run.ID, tc.transition, workflowdef.TransitionActorCoordinator)
			testutil.FailErr(t, "reconcile transition", err)
			if run.CurrentPhase != tc.wantPhase {
				t.Fatalf("phase = %q want %q", run.CurrentPhase, tc.wantPhase)
			}
			if tc.transition != "deepen" {
				return
			}

			vars, err := mgr.Store.GetScaffoldVars(ctx, run.ID)
			testutil.FailErr(t, "get drill vars", err)
			gates, _ := vars["gates"].(map[string]any)
			if planned, _ := gates["fanout_planned"].(bool); planned {
				t.Fatal("drill_plan retained fanout_planned satisfaction")
			}
			run = stampReconWave(t, ctx, mgr, run, projectDir, "Resolve the material scout gap")
			run, err = mgr.Advance(ctx, run.ID)
			testutil.FailErr(t, "advance drill plan", err)
			if run.CurrentPhase != "drill" {
				t.Fatalf("phase = %q want drill", run.CurrentPhase)
			}
			testutil.FailErr(t, "complete drill wave", mgr.RecordWorkerTerminalProof(ctx, run.SessionID, "drill-1", "complete"))
			run, err = mgr.Get(ctx, run.ID)
			testutil.FailErr(t, "get after drill", err)
			if run.CurrentPhase != "report" {
				t.Fatalf("phase = %q want report", run.CurrentPhase)
			}
		})
	}
}

func stampReconWave(t *testing.T, ctx context.Context, mgr *RunManager, run *api.WorkflowRun, projectDir, prompt string) *api.WorkflowRun {
	t.Helper()
	vars, err := mgr.Store.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "get wave vars", err)
	manifest, err := mgr.manifestForRun(ctx, run)
	testutil.FailErr(t, "read wave manifest", err)
	def, _ := manifest.PhaseByID(run.CurrentPhase)
	mgr.WorkerTasks = func(context.Context, string) ([]api.WorkerTask, error) {
		return []api.WorkerTask{{ID: "wave-worker", WorkflowPhase: def.Next, WorkflowWorkID: "leg-1", Status: api.WorkerStatusComplete, Result: &api.WorkerResult{CompletionReport: &api.WorkerCompletionReport{LegStatus: "complete"}}}}, nil
	}
	vars = stampFanoutPlan(vars, FanoutPlan{
		Phase: def.Next, MaxAttempts: 1,
		Legs: []FanoutPlanLeg{{ID: "leg-1", AgentType: "path-explorer", Prompt: prompt}},
	})
	vars = SetGateSatisfied(vars, "fanout_planned", true)
	testutil.FailErr(t, "stamp wave vars", mgr.Store.UpdateVars(ctx, run, projectDir, vars))
	updated, err := mgr.Get(ctx, run.ID)
	testutil.FailErr(t, "reload stamped run", err)
	return updated
}
