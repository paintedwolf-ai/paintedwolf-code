package workflow

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestInitialTerminalPhaseCompletesWithoutCoordinatorHooks(t *testing.T) {
	mgr, _, _, _ := testManager(t)
	manifest := workflowdef.FinalizeManifest(workflowdef.Manifest{
		ID: "already-done", Version: "1.0.0",
		PhaseDefs: []workflowdef.PhaseDef{{ID: "done", Terminal: true, CompleteWhen: "orchestration_complete"}},
	})
	mgr.Manifests = workflowdef.NewRegistry(map[string]workflowdef.Manifest{workflowdef.ManifestKey(manifest.ID, manifest.Version): manifest})
	phaseWakeCount := 0
	phaseEnterCount := 0
	completedIDs := []string{}
	mgr.OnRunCompleted = func(ctx context.Context, run *api.WorkflowRun) {
		stored, err := mgr.Get(ctx, run.ID)
		testutil.FailErr(t, "read notified completion", err)
		if stored.Status != api.WorkflowRunStatusComplete {
			t.Fatalf("completion preceded committed state: %s", stored.Status)
		}
		completedIDs = append(completedIDs, run.ID)
	}
	mgr.OnPhaseAutoAdvanced = func(context.Context, string, string, string, string) { phaseWakeCount++ }
	mgr.PhaseEnterHook = func(context.Context, *RunContext, workflowdef.PhaseDef) { phaseEnterCount++ }

	run, err := startRun(context.Background(), mgr, "sess-1", manifest.ID, manifest.Version)
	testutil.FailErr(t, "start terminal workflow", err)
	if run.Status != api.WorkflowRunStatusComplete || run.CurrentPhase != "done" {
		t.Fatalf("terminal start = status %q phase %q", run.Status, run.CurrentPhase)
	}
	if run.CompletedAt == nil || run.EndMessageID == "" {
		t.Fatalf("terminal start missing completion metadata: %+v", run)
	}
	if len(completedIDs) != 1 || completedIDs[0] != run.ID {
		t.Fatalf("completion notifications = %v, want [%s]", completedIDs, run.ID)
	}
	if phaseWakeCount != 0 || phaseEnterCount != 0 {
		t.Fatalf("terminal start callbacks: phase=%d enter=%d", phaseWakeCount, phaseEnterCount)
	}
}

func TestInitialTerminalChildCompletesAndResumesParentWithoutCoordinatorHooks(t *testing.T) {
	mgr, _, _, _ := testManager(t)
	parentManifest := workflowdef.FinalizeManifest(workflowdef.Manifest{
		ID: "parent", Version: "1.0.0",
		PhaseDefs: []workflowdef.PhaseDef{
			{ID: "work", CompleteWhen: "gates_satisfied", Gates: []string{"stay_open"}, Next: "done"},
			{ID: "done", Terminal: true, CompleteWhen: "orchestration_complete"},
		},
	})
	childManifest := workflowdef.FinalizeManifest(workflowdef.Manifest{
		ID: "empty-child", Version: "1.0.0",
		PhaseDefs: []workflowdef.PhaseDef{{ID: "done", Terminal: true, CompleteWhen: "orchestration_complete"}},
	})
	mgr.Manifests = workflowdef.NewRegistry(map[string]workflowdef.Manifest{
		workflowdef.ManifestKey(parentManifest.ID, parentManifest.Version): parentManifest,
		workflowdef.ManifestKey(childManifest.ID, childManifest.Version):   childManifest,
	})
	parent, err := startRun(context.Background(), mgr, "sess-1", parentManifest.ID, parentManifest.Version)
	testutil.FailErr(t, "start parent", err)
	phaseEnterCount := 0
	completedIDs := []string{}
	mgr.OnRunCompleted = func(ctx context.Context, run *api.WorkflowRun) {
		stored, err := mgr.Get(ctx, run.ID)
		testutil.FailErr(t, "read notified completion", err)
		if stored.Status != api.WorkflowRunStatusComplete {
			t.Fatalf("completion preceded committed state: %s", stored.Status)
		}
		completedIDs = append(completedIDs, run.ID)
	}
	mgr.PhaseEnterHook = func(context.Context, *RunContext, workflowdef.PhaseDef) { phaseEnterCount++ }

	child, err := mgr.InvokeChild(context.Background(), parent.ID, workflowdef.InvokeWorkflowSpec{
		WorkflowID: childManifest.ID,
		Version:    childManifest.Version,
		Blueprint:  workflowdef.ChildBlueprintNone,
	})
	testutil.FailErr(t, "invoke terminal child", err)
	if child.Status != api.WorkflowRunStatusComplete || child.CompletedAt == nil {
		t.Fatalf("terminal child = status %q completed_at %v", child.Status, child.CompletedAt)
	}
	resumed, err := mgr.Get(context.Background(), parent.ID)
	testutil.FailErr(t, "get resumed parent", err)
	if resumed.Status != api.WorkflowRunStatusRunning || resumed.CurrentPhase != "work" {
		t.Fatalf("resumed parent = status %q phase %q", resumed.Status, resumed.CurrentPhase)
	}
	if len(completedIDs) != 1 || completedIDs[0] != child.ID {
		t.Fatalf("completion notifications = %v, want only child %s", completedIDs, child.ID)
	}
	if phaseEnterCount != 0 {
		t.Fatalf("terminal child phase-enter callbacks = %d", phaseEnterCount)
	}
}
