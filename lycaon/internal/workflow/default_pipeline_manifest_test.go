package workflow

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestBugbashManifestBindsAndAdvances(t *testing.T) {
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	testutil.FailErr(t, "conditions.NewDefaultRegistry failed", err)
	manifests, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "RegistryFromDirs failed", err)
	manifest, err := manifests.Get("bugbash", "1.0.0")
	testutil.FailErr(t, "manifests.Get failed", err)

	wantBinds := map[string]string{
		"triage": "triage",
	}
	for phase, stage := range wantBinds {
		def, ok := manifest.PhaseByID(phase)
		if !ok {
			t.Fatalf("missing phase %q", phase)
		}
		if def.BindTopologyStage != stage {
			t.Fatalf("phase %q bind = %q want %q", phase, def.BindTopologyStage, stage)
		}
		if def.CompleteWhen != "topology_stage_complete" {
			t.Fatalf("phase %q complete_when = %q", phase, def.CompleteWhen)
		}
		if !workflowdef.IsKnownCompleteWhen(def.CompleteWhen) {
			t.Fatalf("phase %q unknown complete_when %q", phase, def.CompleteWhen)
		}
	}

	hunt, ok := manifest.PhaseByID("hunt")
	if !ok {
		t.Fatal("missing hunt phase")
	}
	if hunt.CompleteWhen != "parallel_stages_complete" {
		t.Fatalf("hunt complete_when = %q", hunt.CompleteWhen)
	}
	if len(hunt.BindParallelGroup) != 3 {
		t.Fatalf("hunt bind_parallel_group = %v", hunt.BindParallelGroup)
	}

	mgr, _, _, _ := testManager(t)
	mgr.SetConditionRegistry(reg)
	mgr.Manifests = manifests
	ctx := context.Background()
	run, err := startRun(ctx, mgr, "sess-1", "bugbash", "1.0.0")
	testutil.FailErr(t, "startRun failed", err)
	if run.CurrentPhase != "hunt" {
		t.Fatalf("phase = %q want hunt", run.CurrentPhase)
	}

	if _, err := mgr.Advance(ctx, run.ID); err == nil {
		t.Fatal("expected advance blocked before hunt parallel stages complete")
	}
	for _, stage := range []string{"hunt_correctness", "hunt_edges", "hunt_races"} {
		if err := mgr.MarkTopologyStageComplete(ctx, run.ID, stage, "", ""); err != nil {
			testutil.FailErr(t, "MarkTopologyStageComplete "+stage, err)
		}
	}
	run, err = mgr.Get(ctx, run.ID)
	testutil.FailErr(t, "mgr.Get after hunt stages", err)
	if run.CurrentPhase != "triage" {
		t.Fatalf("phase = %q want triage after hunt topology marks", run.CurrentPhase)
	}

	eval := RegistryGateEvaluator{Registry: reg, Sessions: mgr.Sessions}
	vars, err := mgr.Store.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "mgr.Store.GetScaffoldVars failed", err)
	okGate, _, err := eval.PhaseGateMet(ctx, manifest, run, vars)
	if err != nil || okGate {
		t.Fatalf("triage gate before stage complete = %v err=%v", okGate, err)
	}
	if err := mgr.MarkTopologyStageComplete(ctx, run.ID, "triage", "", ""); err != nil {
		testutil.FailErr(t, "mgr.MarkTopologyStageComplete failed", err)
	}
	run, err = mgr.Get(ctx, run.ID)
	testutil.FailErr(t, "mgr.Get after triage stage", err)
	if run.CurrentPhase != "expand" {
		t.Fatalf("phase = %q want expand after triage topology mark", run.CurrentPhase)
	}
}

func TestBugbashParallelStagesGate(t *testing.T) {
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	testutil.FailErr(t, "conditions.NewDefaultRegistry failed", err)
	eval := RegistryGateEvaluator{Registry: reg}
	manifest := workflowdef.Manifest{
		PhaseDefs: []workflowdef.PhaseDef{{
			ID:                "review_test",
			CompleteWhen:      "parallel_stages_complete",
			BindParallelGroup: []string{"review", "test"},
		}},
	}
	run := &api.WorkflowRun{CurrentPhase: "review_test", SessionID: "s1", Status: api.WorkflowRunStatusRunning}
	vars := map[string]any{
		"topology_stages": map[string]any{
			"review": map[string]any{"complete": true},
		},
	}
	ok, _, err := eval.PhaseGateMet(context.Background(), manifest, run, vars)
	if err != nil || ok {
		t.Fatalf("parallel gate with one stage = %v err=%v", ok, err)
	}
	vars["topology_stages"] = map[string]any{
		"review": map[string]any{"complete": true},
		"test":   map[string]any{"complete": true},
	}
	ok, _, err = eval.PhaseGateMet(context.Background(), manifest, run, vars)
	if err != nil || !ok {
		t.Fatalf("parallel gate with both stages = %v err=%v", ok, err)
	}
}

func TestBugbashApprovedBlueprintRunsImplementChildAndCompletes(t *testing.T) {
	mgr, _, blueprintMgr, projectDir := testManager(t)
	delivered := false
	deps := conditions.TestRegistryDepsWithEvidence()
	deps.DeliveryReported = func(context.Context, string, string, string) (bool, error) { return delivered, nil }
	setTestRegistry(t, mgr, blueprintMgr, deps)
	ctx := workflowCaller(t, mgr)

	parent, err := startRun(ctx, mgr, "sess-1", "bugbash", "1.0.0")
	testutil.FailErr(t, "start bugbash", err)
	for _, stage := range []string{"hunt_correctness", "hunt_edges", "hunt_races", "triage"} {
		testutil.FailErr(t, "complete topology stage "+stage, mgr.MarkTopologyStageComplete(ctx, parent.ID, stage, stage+" complete", ""))
	}
	parent, err = mgr.Get(ctx, parent.ID)
	testutil.FailErr(t, "get expand phase", err)
	if parent.CurrentPhase != "expand" {
		t.Fatalf("phase = %q want expand", parent.CurrentPhase)
	}
	seedValidPlanContent(t, blueprintMgr, parent.BlueprintPath)
	parent, err = mgr.TryAutoAdvance(ctx, parent.ID)
	testutil.FailErr(t, "advance to approval", err)
	if parent.CurrentPhase != "approve" {
		t.Fatalf("phase = %q want approve", parent.CurrentPhase)
	}
	parent, err = mgr.SyncHumanApproval(ctx, parent.ID, projectDir)
	testutil.FailErr(t, "approve bugbash Blueprint", err)
	if parent.Status != api.WorkflowRunStatusPausedOnChild || parent.CurrentPhase != "fix" {
		t.Fatalf("parent = status %q phase %q want paused_on_child/fix", parent.Status, parent.CurrentPhase)
	}
	child, err := mgr.GetActive(ctx, parent.SessionID)
	testutil.FailErr(t, "get implement child", err)
	if child.WorkflowID != "implement" || child.BlueprintPath != parent.BlueprintPath {
		t.Fatalf("child = workflow %q Blueprint %q want implement/%q", child.WorkflowID, child.BlueprintPath, parent.BlueprintPath)
	}
	msgs, err := mgr.Sessions.GetMessages(ctx, parent.SessionID)
	testutil.FailErr(t, "get Blueprint transcript while child runs", err)
	blueprintRow, ok := findBlueprintTranscriptMessage(msgs, parent.BlueprintPath)
	if !ok || blueprintRow.Blueprint == nil {
		t.Fatal("expected Blueprint transcript while child runs")
	}
	if blueprintRow.Blueprint.PhaseLabel != blueprintPhaseLabel(parent.CurrentPhase) {
		t.Fatalf("Blueprint phase label = %q want parent phase %q", blueprintRow.Blueprint.PhaseLabel, parent.CurrentPhase)
	}
	testutil.FailErr(t, "orient implement child", mgr.RecordBoardOrientReady(ctx, child.SessionID, "test-orient"))
	child, err = mgr.Get(ctx, child.ID)
	testutil.FailErr(t, "get child before delivery", err)
	if child.Status != api.WorkflowRunStatusRunning || child.CurrentPhase != "work" {
		t.Fatalf("child before delivery = %q/%q, want running/work", child.Status, child.CurrentPhase)
	}
	delivered = true
	testutil.FailErr(t, "complete implement child work", mgr.RecordWorkerTerminalProof(ctx, child.SessionID, "test-work", "complete"))

	child, err = mgr.Get(ctx, child.ID)
	testutil.FailErr(t, "get completed child", err)
	if child.Status != api.WorkflowRunStatusComplete {
		t.Fatalf("child status = %q phase=%q want complete", child.Status, child.CurrentPhase)
	}
	parent, err = mgr.Get(ctx, parent.ID)
	testutil.FailErr(t, "get completed bugbash", err)
	if parent.Status != api.WorkflowRunStatusComplete || parent.CurrentPhase != "done" {
		t.Fatalf("parent = status %q phase %q want complete/done", parent.Status, parent.CurrentPhase)
	}
	msgs, err = mgr.Sessions.GetMessages(ctx, parent.SessionID)
	testutil.FailErr(t, "get completed Blueprint transcript", err)
	blueprintRow, ok = findBlueprintTranscriptMessage(msgs, parent.BlueprintPath)
	if !ok || blueprintRow.Blueprint == nil || blueprintRow.Blueprint.PhaseLabel != "Done" {
		t.Fatalf("completed Blueprint transcript = %+v want Done", blueprintRow.Blueprint)
	}
}
