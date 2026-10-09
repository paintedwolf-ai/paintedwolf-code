package phases_test

import (
	"context"
	"encoding/json"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowphases "github.com/lycaon/lycaon/internal/workflow/phases"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"testing"
)

// TestAutoAdvanceMarkerLifecycle covers the host-auto-advance marker
// (workflowphases.HostAutoAdvancedFromKey): stamped by TryAutoAdvance, consumed by exactly one
// workflow_advance, observable to no other code.
func TestAutoAdvanceMarkerLifecycle(t *testing.T) {
	t.Run("auto-advance stamps marker; coordinator phase consumes it", func(t *testing.T) {
		mgr, _, _, _ := testManagerWithRegistry(t)
		reg := registerAdvanceToolTestRegistry(t, mgr)
		ctx := context.Background()
		manifest := workflowdef.FinalizeManifest(workflowdef.Manifest{
			ID:      "marker-coord",
			Version: "1.0.0",
			PhaseDefs: []workflowdef.PhaseDef{{
				ID:                 "stub",
				Next:               "research",
				AdvanceWhenGateMet: workflowdef.AdvanceWhenGateMetCoordinator,
			}, {ID: "research"}},
		})
		mgr.Resolver.Overlay = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"marker-coord@1.0.0": manifest})
		run, err := startRun(ctx, mgr, "sess-1", "marker-coord", "1.0.0")
		testutil.FailErr(t, "startRun failed", err)

		vars := runstate.SetHostVar(nil, workflowphases.HostAutoAdvancedFromKey, "stub")
		projectDir := mgr.Resolver.ProjectDirForRun(ctx, run)
		if err := mgr.Store.State.UpdateVars(ctx, run, projectDir, vars); err != nil {
			testutil.FailErr(t, "UpdateVars", err)
		}

		first := runAdvanceTool(t, reg, projectDir)
		if !first.AlreadyAdvanced {
			t.Fatalf("coordinator phase with marker should report already_advanced; got %+v", first)
		}

		varsAfter, err := mgr.Store.Runs.GetScaffoldVars(ctx, run.ID)
		testutil.FailErr(t, "mgr.Store.Runs.GetScaffoldVars failed", err)
		if _, ok := varsAfter[workflowphases.HostAutoAdvancedFromKey]; ok {
			t.Fatal("workflowphases.HostAutoAdvancedFromKey must be cleared after workflow_advance returns already_advanced")
		}
	})

	t.Run("auto phase rejects workflow_advance; marker preserved", func(t *testing.T) {
		mgr, _, blueprintMgr, projectDir := testManagerWithRegistry(t)
		reg := registerAdvanceToolTestRegistry(t, mgr)
		ctx := context.Background()
		run, err := startRun(ctx, mgr, "sess-1", "plan", "1.0.0")
		testutil.FailErr(t, "startRun failed", err)
		run = completePlanIntakeT(ctx, t, mgr, run)
		run, err = completePlanResearchAtDepthNone(ctx, mgr, run)
		testutil.FailErr(t, "complete research at depth none", err)
		seedValidPlanContent(t, blueprintMgr, run.BlueprintPath)
		if run.CurrentPhase != "expand" {
			t.Fatalf("phase = %q want expand", run.CurrentPhase)
		}
		vars, err := mgr.Store.Runs.GetScaffoldVars(ctx, run.ID)
		testutil.FailErr(t, "GetScaffoldVars", err)
		delete(vars, workflowphases.HostAutoAdvancedFromKey)
		if err := mgr.Store.State.UpdateVars(ctx, run, projectDir, vars); err != nil {
			testutil.FailErr(t, "UpdateVars", err)
		}

		result := runAdvanceTool(t, reg, projectDir)
		if result.Error != "advance_not_coordinator_mode" {
			t.Fatalf("auto phase must reject workflow_advance; got %+v", result)
		}
		varsAfter, err := mgr.Store.Runs.GetScaffoldVars(ctx, run.ID)
		testutil.FailErr(t, "mgr.Store.Runs.GetScaffoldVars failed", err)
		if _, ok := varsAfter[workflowphases.HostAutoAdvancedFromKey]; ok {
			t.Fatal("reject must not stamp or consume workflowphases.HostAutoAdvancedFromKey")
		}
	})

	t.Run("pending feedback takes precedence over stamped marker", func(t *testing.T) {
		mgr, _, _, _ := testManagerWithRegistry(t)
		reg := registerAdvanceToolTestRegistry(t, mgr)
		ctx := context.Background()
		manifest := workflowdef.FinalizeManifest(workflowdef.Manifest{
			ID:      "marker-pending",
			Version: "1.0.0",
			PhaseDefs: []workflowdef.PhaseDef{{
				ID:                 "stub",
				Next:               "research",
				AdvanceWhenGateMet: workflowdef.AdvanceWhenGateMetCoordinator,
			}, {ID: "research"}},
		})
		mgr.Resolver.Overlay = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"marker-pending@1.0.0": manifest})
		run, err := startRun(ctx, mgr, "sess-1", "marker-pending", "1.0.0")
		testutil.FailErr(t, "startRun failed", err)

		vars := runstate.SetHostVar(nil, workflowphases.HostAutoAdvancedFromKey, "stub")
		vars = withPendingFeedback(vars, run.CurrentPhase, "clarify scope")
		projectDir := mgr.Resolver.ProjectDirForRun(ctx, run)
		if err := mgr.Store.State.UpdateVars(ctx, run, projectDir, vars); err != nil {
			testutil.FailErr(t, "mgr.Store.State.UpdateVars failed", err)
		}

		result := runAdvanceTool(t, reg, projectDir)
		if result.Error != "pending_user_input" {
			t.Fatalf("pending feedback must win over already_advanced; got error=%q already_advanced=%v", result.Error, result.AlreadyAdvanced)
		}
		if result.AlreadyAdvanced {
			t.Fatal("AlreadyAdvanced must be false when pending input wins")
		}

		varsAfter, err := mgr.Store.Runs.GetScaffoldVars(ctx, run.ID)
		testutil.FailErr(t, "mgr.Store.Runs.GetScaffoldVars failed", err)
		if _, ok := varsAfter[workflowphases.HostAutoAdvancedFromKey]; !ok {
			t.Fatal("workflowphases.HostAutoAdvancedFromKey must survive a pending_user_input response (single-shot semantics)")
		}
	})

	t.Run("manual Advance does not stamp the marker", func(t *testing.T) {
		// Only automatic advances write the marker.
		mgr, _, blueprintMgr, _ := testManagerWithRegistry(t)
		ctx := context.Background()
		run, err := startRun(ctx, mgr, "sess-1", "plan", "1.0.0")
		testutil.FailErr(t, "startRun failed", err)
		run = completePlanIntakeT(ctx, t, mgr, run)
		run, err = completePlanResearchAtDepthNone(ctx, mgr, run)
		testutil.FailErr(t, "complete research at depth none", err)
		seedValidPlanContent(t, blueprintMgr, run.BlueprintPath)
		vars, err := mgr.Store.Runs.GetScaffoldVars(ctx, run.ID)
		testutil.FailErr(t, "GetScaffoldVars failed", err)
		delete(vars, workflowphases.HostAutoAdvancedFromKey)
		projectDir := mgr.Resolver.ProjectDirForRun(ctx, run)
		if err := mgr.Store.State.UpdateVars(ctx, run, projectDir, vars); err != nil {
			testutil.FailErr(t, "UpdateVars", err)
		}

		if _, err := mgr.Phases.Advance(ctx, run.ID); err != nil {
			testutil.FailErr(t, "mgr.Phases.Advance failed", err)
		}
		vars, err = mgr.Store.Runs.GetScaffoldVars(ctx, run.ID)
		testutil.FailErr(t, "mgr.Store.Runs.GetScaffoldVars failed", err)
		if _, ok := vars[workflowphases.HostAutoAdvancedFromKey]; ok {
			t.Fatal("manual Advance must not stamp workflowphases.HostAutoAdvancedFromKey")
		}
	})
}

func runAdvanceTool(t *testing.T, reg *tools.DefaultRegistry, projectDir string) workflowphases.AdvanceToolResult {
	t.Helper()
	out, err := reg.Run(context.Background(), "workflow_advance", map[string]any{}, tools.ToolContext{
		Roots:        []projectroot.RootRef{{ID: "r1", Label: "root", Path: projectDir, IsPrimary: true}},
		ActiveRootID: "r1",
		Agent:        "coordinator",
		SessionID:    "sess-1",
	})
	if err != nil {
		t.Fatalf("workflow_advance returned bare error: %v", err)
	}
	var result workflowphases.AdvanceToolResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("decode result: %v body = %s", err, out)
	}
	return result
}

func withPendingFeedback(vars map[string]any, phaseID, prompt string) map[string]any {
	vars = runstate.CloneVars(vars)
	bucket, _ := vars["user_feedback"].(map[string]any)
	if bucket == nil {
		bucket = map[string]any{}
		vars["user_feedback"] = bucket
	}
	bucket[phaseID] = map[string]any{
		"pending": true,
		"prompt":  prompt,
	}
	return vars
}

// TestAdvanceMarkerConsumeReplaysThroughCommandJournal: consuming the marker rides the
// command journal, so a duplicate workflow_advance with the same ToolCallID replays the
// AlreadyAdvanced receipt instead of performing a second real Advance.
func TestAdvanceMarkerConsumeReplaysThroughCommandJournal(t *testing.T) {
	mgr, _, _, _ := testManagerWithRegistry(t)
	reg := registerAdvanceToolTestRegistry(t, mgr)
	ctx := context.Background()
	manifest := workflowdef.FinalizeManifest(workflowdef.Manifest{
		ID:      "marker-replay",
		Version: "1.0.0",
		PhaseDefs: []workflowdef.PhaseDef{{
			ID:                 "stub",
			Next:               "research",
			AdvanceWhenGateMet: workflowdef.AdvanceWhenGateMetCoordinator,
		}, {ID: "research"}},
	})
	mgr.Resolver.Overlay = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"marker-replay@1.0.0": manifest})
	run, err := startRun(ctx, mgr, "sess-1", "marker-replay", "1.0.0")
	testutil.FailErr(t, "startRun failed", err)

	vars := runstate.SetHostVar(nil, workflowphases.HostAutoAdvancedFromKey, "stub")
	projectDir := mgr.Resolver.ProjectDirForRun(ctx, run)
	testutil.FailErr(t, "UpdateVars", mgr.Store.State.UpdateVars(ctx, run, projectDir, vars))

	first := runAdvanceToolWithCallID(t, reg, projectDir, "advance-call-1")
	if !first.AlreadyAdvanced {
		t.Fatalf("first call should consume the marker; got %+v", first)
	}

	replayed := runAdvanceToolWithCallID(t, reg, projectDir, "advance-call-1")
	if !replayed.AlreadyAdvanced {
		t.Fatalf("duplicate ToolCallID must replay the AlreadyAdvanced receipt, not advance; got %+v", replayed)
	}
	after, err := mgr.Store.Runs.Get(ctx, run.ID)
	testutil.FailErr(t, "get run", err)
	if after.CurrentPhase != "stub" {
		t.Fatalf("phase = %q — the replayed duplicate performed a real advance", after.CurrentPhase)
	}

	// A fresh ToolCallID after the marker cleared performs the real advance.
	next := runAdvanceToolWithCallID(t, reg, projectDir, "advance-call-2")
	if next.AlreadyAdvanced || next.Run == nil || next.Run.CurrentPhase != "research" {
		t.Fatalf("fresh call should advance for real; got %+v", next)
	}
}

func runAdvanceToolWithCallID(t *testing.T, reg *tools.DefaultRegistry, projectDir, toolCallID string) workflowphases.AdvanceToolResult {
	t.Helper()
	out, err := reg.Run(context.Background(), "workflow_advance", map[string]any{}, tools.ToolContext{
		Roots:        []projectroot.RootRef{{ID: "r1", Label: "root", Path: projectDir, IsPrimary: true}},
		ActiveRootID: "r1",
		Agent:        "coordinator",
		SessionID:    "sess-1",
		ToolCallID:   toolCallID,
	})
	if err != nil {
		t.Fatalf("workflow_advance returned bare error: %v", err)
	}
	var result workflowphases.AdvanceToolResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("decode result: %v body = %s", err, out)
	}
	return result
}
