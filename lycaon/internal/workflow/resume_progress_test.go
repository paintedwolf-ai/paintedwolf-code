package workflow

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
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
				run, err := mgr.StartHuman(ctx, "sess-1", api.StartWorkflowRunRequest{
					WorkflowID: tc.workflow, WorkflowVersion: "1.0.0", Request: "Inspect the fixture.",
				})
				testutil.FailErr(t, "start run", err)
				phase := run.CurrentPhase
				_, err = mgr.Pause(ctx, run.ID, "review")
				testutil.FailErr(t, "pause run", err)
				for _, stage := range tc.stages {
					testutil.FailErr(t, "complete paused stage", mgr.MarkTopologyStageComplete(ctx, run.ID, stage, "observed result", ""))
				}
				paused, err := mgr.Get(ctx, run.ID)
				testutil.FailErr(t, "read paused run", err)
				if paused.Status != api.WorkflowRunStatusPaused || paused.CurrentPhase != phase {
					t.Fatalf("paused run moved: %+v", paused)
				}
				if restart {
					fresh := NewManager(mgr.Store, mgr.Sessions, mgr.Manifests, nil)
					fresh.SetConditionRegistry(mgr.Registry)
					fresh.BlueprintGet = mgr.BlueprintGet
					fresh.BlueprintCreate = mgr.BlueprintCreate
					fresh.SessionScaffold = mgr.SessionScaffold
					mgr = fresh
				}
				wakes, resumedWakes := 0, 0
				mgr.OnPhaseAutoAdvanced = func(context.Context, string, string, string, string) { wakes++ }
				mgr.OnRunResumed = func(context.Context, *api.WorkflowRun) { resumedWakes++ }
				commandCtx := WithExpectedRevision(ctx, paused.Revision)
				resumed, err := mgr.Resume(commandCtx, run.ID)
				testutil.FailErr(t, "resume completed phase", err)
				if resumed.CurrentPhase != tc.next || resumed.Status != api.WorkflowRunStatusRunning {
					t.Fatalf("resumed phase=%s status=%s, want %s running", resumed.CurrentPhase, resumed.Status, tc.next)
				}
				if wakes != 1 || resumedWakes != 0 {
					t.Fatalf("phase wakes=%d resume wakes=%d, want one phase wake", wakes, resumedWakes)
				}
				_, err = mgr.Resume(commandCtx, run.ID)
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
	run, err := mgr.StartHuman(ctx, "sess-1", api.StartWorkflowRunRequest{
		WorkflowID: "options", WorkflowVersion: "1.0.0", Request: "Inspect the fixture.",
	})
	testutil.FailErr(t, "start run", err)
	paused, err := mgr.Pause(ctx, run.ID, "review")
	testutil.FailErr(t, "pause run", err)
	entries, wakes := 0, 0
	mgr.PhaseEnterHook = func(context.Context, *RunContext, workflowdef.PhaseDef) { entries++ }
	mgr.OnRunResumed = func(ctx context.Context, resumed *api.WorkflowRun) {
		wakes++
		held, holdErr := mgr.HostObligationHeld(ctx, resumed.SessionID)
		testutil.FailErr(t, "read resumed host hold", holdErr)
		if !held {
			t.Fatal("unfinished topology lost its hold")
		}
	}
	commandCtx := WithExpectedRevision(ctx, paused.Revision)
	resumed, err := mgr.Resume(commandCtx, run.ID)
	testutil.FailErr(t, "resume unfinished phase", err)
	if resumed.CurrentPhase != paused.CurrentPhase || wakes != 1 || entries != 0 {
		t.Fatalf("phase=%s wakes=%d entries=%d", resumed.CurrentPhase, wakes, entries)
	}
	_, err = mgr.Resume(commandCtx, run.ID)
	testutil.FailErr(t, "replay resume", err)
	if wakes != 1 || entries != 0 {
		t.Fatal("resume replay restarted the phase")
	}
}
