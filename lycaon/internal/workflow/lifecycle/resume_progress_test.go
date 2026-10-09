package lifecycle_test

import (
	"context"
	"github.com/lycaon/lycaon/internal/testutil"
	workflow "github.com/lycaon/lycaon/internal/workflow"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowphases "github.com/lycaon/lycaon/internal/workflow/phases"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
)

func TestResumeReconcilesTopologyCompletedWhilePaused(t *testing.T) {
	for _, tc := range []struct {
		workflow string
		stages   []string
		next     string
	}{
		{"options", []string{"fan_out"}, "judge"},
		{"bugbash", []string{"hunt_correctness", "hunt_edges", "hunt_races"}, "triage"},
	} {
		for _, restart := range []bool{false, true} {
			name := tc.workflow + "/same_process"
			if restart {
				name = tc.workflow + "/new_manager"
			}
			t.Run(name, func(t *testing.T) {
				mgr, _, _, _ := testManagerWithRegistry(t)
				ctx := t.Context()
				run, err := mgr.Starts.StartHuman(ctx, "sess-1", api.StartWorkflowRunRequest{
					WorkflowID: tc.workflow, WorkflowVersion: "1.0.0", Request: "Inspect the fixture.",
				})
				testutil.FailErr(t, "start run", err)
				phase := run.CurrentPhase
				_, err = mgr.Controls.Pause(ctx, run.ID, "review")
				testutil.FailErr(t, "pause run", err)
				for _, stage := range tc.stages {
					testutil.FailErr(t, "complete paused stage", mgr.Phases.MarkTopologyStageComplete(ctx, run.ID, stage, "observed result", ""))
				}
				paused, err := mgr.Store.Runs.Get(ctx, run.ID)
				testutil.FailErr(t, "read paused run", err)
				if paused.Status != api.WorkflowRunStatusPaused || paused.CurrentPhase != phase {
					t.Fatalf("paused run moved: %+v", paused)
				}
				if restart {
					fresh := workflow.NewManager(mgr.Store, mgr.Verdicts.Sessions, mgr.Resolver.Overlay, nil)
					fresh.SetConditionRegistry(mgr.Policy.Registry)
					fresh.Blueprints.Getter = mgr.Blueprints.Getter
					fresh.Presentation.BlueprintGetter = mgr.Blueprints.Getter
					fresh.Approvals.Getter = mgr.Approvals.Getter
					fresh.Blueprints.Creator = mgr.Blueprints.Creator
					fresh.Blueprints.Scaffold.Store = mgr.Blueprints.Scaffold.Store
					mgr = fresh
				}
				wakes, resumedWakes := 0, 0
				mgr.Publication.OnPhaseAutoAdvanced = func(context.Context, string, string, string, string) { wakes++ }
				mgr.Controls.OnRunResumed = func(context.Context, *api.WorkflowRun) { resumedWakes++ }
				commandCtx := runstate.WithExpectedRevision(ctx, paused.Revision)
				resumed, err := mgr.Controls.Resume(commandCtx, run.ID)
				testutil.FailErr(t, "resume completed phase", err)
				if resumed.CurrentPhase != tc.next || resumed.Status != api.WorkflowRunStatusRunning {
					t.Fatalf("resumed phase=%s status=%s, want %s running", resumed.CurrentPhase, resumed.Status, tc.next)
				}
				if wakes != 1 || resumedWakes != 0 {
					t.Fatalf("phase wakes=%d resume wakes=%d, want one phase wake", wakes, resumedWakes)
				}
				_, err = mgr.Controls.Resume(commandCtx, run.ID)
				testutil.FailErr(t, "replay resume", err)
				if wakes != 1 || resumedWakes != 0 {
					t.Fatal("resume replay repeated work")
				}
			})
		}
	}
}

func TestResumeRetainsUnfinishedPhaseWithoutReenteringIt(t *testing.T) {
	mgr, _, _, _ := testManagerWithRegistry(t)
	ctx := t.Context()
	run, err := mgr.Starts.StartHuman(ctx, "sess-1", api.StartWorkflowRunRequest{
		WorkflowID: "options", WorkflowVersion: "1.0.0", Request: "Inspect the fixture.",
	})
	testutil.FailErr(t, "start run", err)
	paused, err := mgr.Controls.Pause(ctx, run.ID, "review")
	testutil.FailErr(t, "pause run", err)
	entries, wakes := 0, 0
	mgr.Phases.PhaseEnterHook = func(context.Context, *workflowphases.RunContext, workflowdef.PhaseDef) { entries++ }
	mgr.Controls.OnRunResumed = func(ctx context.Context, resumed *api.WorkflowRun) {
		wakes++
		held, holdErr := mgr.Obligations.HostObligationHeld(ctx, resumed.SessionID)
		testutil.FailErr(t, "read resumed host hold", holdErr)
		if !held {
			t.Fatal("unfinished topology lost its hold")
		}
	}
	commandCtx := runstate.WithExpectedRevision(ctx, paused.Revision)
	resumed, err := mgr.Controls.Resume(commandCtx, run.ID)
	testutil.FailErr(t, "resume unfinished phase", err)
	if resumed.CurrentPhase != paused.CurrentPhase || wakes != 1 || entries != 0 {
		t.Fatalf("phase=%s wakes=%d entries=%d", resumed.CurrentPhase, wakes, entries)
	}
	_, err = mgr.Controls.Resume(commandCtx, run.ID)
	testutil.FailErr(t, "replay resume", err)
	if wakes != 1 || entries != 0 {
		t.Fatal("resume replay restarted the phase")
	}
}
