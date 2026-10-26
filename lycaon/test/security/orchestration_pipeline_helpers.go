package security

import (
	"context"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workflow"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

type orchestrationPipelineHarness struct {
	srv         *api.Server
	workflowMgr *workflow.RunManager
	ownerCtx    context.Context
	hub         *events.MemoryHub
	sess        wire.Session
	projectDir  string
}

func startBugcommandRun(t *testing.T, h *orchestrationPipelineHarness) wire.WorkflowRun {
	t.Helper()
	run, err := h.workflowMgr.Starts.StartHuman(context.Background(), h.sess.ID, wire.StartWorkflowRunRequest{
		WorkflowID: "bugbash", WorkflowVersion: "1.1.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	h.srv.Admin.Workflow.Topology.StartOrchestratedTopologyForRun(context.Background(), h.sess.ID, run)
	return *run
}

func newOrchestrationPipelineServer(t *testing.T, projectDir string) *orchestrationPipelineHarness {
	t.Helper()
	h := wiring.BuildForTest(t, wiring.WithAutoCompleteDelegation())
	sess := createSessionHTTP(t, h.Server, projectDir)
	return &orchestrationPipelineHarness{
		srv:         h.Server,
		workflowMgr: h.Workflows.Manager,
		ownerCtx:    h.OwnerCtx(t, context.Background()),
		hub:         h.MemoryHub(),
		sess:        sess,
		projectDir:  projectDir,
	}
}

func completeActiveChildRun(t *testing.T, h *orchestrationPipelineHarness) {
	t.Helper()
	ctx := t.Context()
	child, err := h.workflowMgr.Store.Runs.ActiveBySession(ctx, h.sess.ID)
	testutil.FailErr(t, "get active child run", err)
	if child == nil || child.ParentRunID == nil {
		t.Fatalf("active run = %+v, want child", child)
	}
	now := time.Now().UTC()
	child.Status = wire.WorkflowRunStatusComplete
	child.CompletedAt = &now
	child.UpdatedAt = now
	testutil.FailErr(t, "complete child run", h.workflowMgr.Store.State.Update(ctx, child))
	testutil.FailErr(t, "resume parent run", h.workflowMgr.Children.ReconcileTerminalRun(ctx, child))
}

func waitTopologyStageComplete(t *testing.T, mgr *workflow.RunManager, runID, stage string) {
	t.Helper()
	testutil.WaitFor(t, func() bool {
		vars, err := mgr.Store.Runs.GetScaffoldVars(context.Background(), runID)
		if err != nil {
			return false
		}
		stages, _ := vars["topology_stages"].(map[string]any)
		entry, _ := stages[stage].(map[string]any)
		return entry != nil && entry["complete"] == true
	})

}

func waitWorkflowPhase(t *testing.T, mgr *workflow.RunManager, runID, phase string) {
	t.Helper()
	testutil.WaitFor(t, func() bool {
		run, err := mgr.Store.Runs.Get(context.Background(), runID)
		if err != nil || run == nil {
			return false
		}
		return run.CurrentPhase == phase
	})

}
