package orchestration_test

import (
	"context"
	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/extpacks"
	"testing"

	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestOrchestratorFanOutMarksWorkflowStage(t *testing.T) {
	ctx := context.Background()
	rec := &recordingDelegation{order: make([]string, 0, 4)}
	spy := &spyWorkflowRuns{}
	orch, wfMgr, sessMgr, _, sqlDB, dir := newWorkflowOrchestrator(t, rec, spy)
	sess := createOrchestrateSession(t, sqlDB, sessMgr, dir)

	wfRun, err := wfMgr.Starts.StartHuman(ctx, sess.ID, api.StartWorkflowRunRequest{
		WorkflowID:      "recon-pack",
		WorkflowVersion: "1.0.0",
	})
	testutil.FailErr(t, "wfMgr.Starts.StartHuman failed", err)

	var markedStage, markedOutput string
	spy.afterMark = func(_ context.Context, runID, stage, output string) {
		if runID != wfRun.ID {
			return
		}
		markedStage = stage
		markedOutput = output
	}

	spec, err := orchestration.LoadTopologyFromFile(extpacks.Bundled(config.PlatformFlows.Join("_topologies", "fan-out-recon.yaml")))
	testutil.FailErr(t, "LoadTopologyFromFile failed", err)

	_, err = orch.Run(ctx, orchestration.RunRequest{
		SessionID: sess.ID,
		Topology:  *spec,
		Input: map[string]any{
			"project_dir":      dir,
			"project_id":       testdbseed.DefaultProjectID,
			"workflow_run_id":  wfRun.ID,
			"workflow_id":      "recon-pack",
			"workflow_version": "1.0.0",
		},
	})
	testutil.FailErr(t, "orch.Run failed", err)

	if markedStage != orchestration.TopologyBindStageFanOut {
		t.Fatalf("marked stage = %q want %q", markedStage, orchestration.TopologyBindStageFanOut)
	}
	if markedOutput == "" {
		t.Fatal("expected non-empty topology stage output")
	}

	vars, err := wfMgr.Store.Runs.GetScaffoldVars(ctx, wfRun.ID)
	testutil.FailErr(t, "GetScaffoldVars failed", err)
	stages, _ := vars["topology_stages"].(map[string]any)
	entry, _ := stages["fan_out"].(map[string]any)
	if entry == nil || entry["complete"] != true {
		t.Fatalf("topology_stages.fan_out = %v", stages)
	}
}
