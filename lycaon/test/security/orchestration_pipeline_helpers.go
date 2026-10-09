package security

import (
	"context"
	"github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workflow"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
	"testing"
	"time"
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
		WorkflowID: "bugbash", WorkflowVersion: "1.0.0",
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
		workflowMgr: h.WorkflowMgr,
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
	testutil.FailErr(t, "complete child run", h.workflowMgr.Store.Update(ctx, child))
	testutil.FailErr(t, "resume parent run", h.workflowMgr.Children.ReconcileTerminalRun(ctx, child))
}

func waitTopologyStageComplete(t *testing.T, mgr *workflow.RunManager, runID, stage string) {
	t.Helper()
	var lastStages any
	ok := testutil.WaitForNoFatal(15*time.Second, func() bool {
		vars, err := mgr.Store.Runs.GetScaffoldVars(context.Background(), runID)
		if err != nil {
			return false
		}
		lastStages = vars["topology_stages"]
		stages, _ := vars["topology_stages"].(map[string]any)
		entry, _ := stages[stage].(map[string]any)
		return entry != nil && entry["complete"] == true
	})
	if !ok {
		run, _ := mgr.Store.Runs.Get(context.Background(), runID)
		phase := ""
		if run != nil {
			phase = run.CurrentPhase
		}
		t.Fatalf("topology stage %q never completed; phase = %q, topology_stages = %+v",
			stage, phase, lastStages)
	}
}

func waitWorkflowPhase(t *testing.T, mgr *workflow.RunManager, runID, phase string) {
	t.Helper()
	var last string
	ok := testutil.WaitForNoFatal(15*time.Second, func() bool {
		run, err := mgr.Store.Runs.Get(context.Background(), runID)
		if err != nil || run == nil {
			return false
		}
		last = run.CurrentPhase
		return run.CurrentPhase == phase
	})
	if !ok {
		vars, _ := mgr.Store.Runs.GetScaffoldVars(context.Background(), runID)
		t.Fatalf("workflow never reached phase %q; stuck at %q, topology_stages = %+v",
			phase, last, vars["topology_stages"])
	}
}
