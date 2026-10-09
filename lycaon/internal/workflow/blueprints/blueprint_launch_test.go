package blueprints_test

import (
	"context"
	"github.com/lycaon/lycaon/internal/blueprint"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowblueprints "github.com/lycaon/lycaon/internal/workflow/blueprints"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
)

func TestLaunchSourceCompatible(t *testing.T) {
	if !workflowblueprints.LaunchSourceCompatible(settingsoverlay.Rel("blueprints/a.md")) {
		t.Fatal("expected convention path compatible")
	}
	if workflowblueprints.LaunchSourceCompatible("@plan") {
		t.Fatal("@plan must be rejected")
	}
	if workflowblueprints.LaunchSourceCompatible(settingsoverlay.Rel("options-selection.md")) {
		t.Fatal("non-convention path must be rejected")
	}
}

func TestLaunchFromBlueprintCopiesFile(t *testing.T) {
	mgr, sessionID, projectDir := testWorkflowManager(t)
	ctx := context.Background()
	run, err := mgr.Starts.StartHuman(ctx, sessionID, api.StartWorkflowRunRequest{
		WorkflowID: "plan", WorkflowVersion: "1.0.0",
	})
	testutil.FailErr(t, "StartHuman", err)
	blueprintMgr, ok := mgr.Blueprints.Getter.(*blueprint.Manager)
	if !ok || blueprintMgr == nil {
		t.Fatal("expected *blueprint.Manager BlueprintGet")
	}
	source, err := blueprintMgr.Get(ctx, run.ProjectID, run.BlueprintPath)
	testutil.FailErr(t, "Get source", err)
	content := blueprint.ResetToDraftFrontmatter("# Source\n\n## Goal\nkeep\n")
	source, err = blueprintMgr.Update(ctx, run.ProjectID, source.Path, &content, nil)
	testutil.FailErr(t, "Update source", err)

	target, err := workflowblueprints.ResolveLaunchTarget(source.Path, "plan", mgr.Resolver.Overlay.List())
	testutil.FailErr(t, "workflowblueprints.ResolveLaunchTarget", err)

	sess2, err := mgr.Verdicts.Sessions.Create(ctx, api.CreateSessionRequest{
		ProjectID: run.ProjectID,
		Posture:   api.SessionPostureSpec,
	}, run.ProjectID)
	testutil.FailErr(t, "Create session", err)
	launched, seed, err := mgr.Blueprints.LaunchFromBlueprint(ctx, sess2.ID, source, target, blueprintMgr, projectDir, false)
	testutil.FailErr(t, "LaunchFromBlueprint", err)
	if seed.Path == source.Path {
		t.Fatal("launch must mint a new path")
	}
	if launched == nil || launched.BlueprintPath != seed.Path {
		t.Fatalf("run path = %q want %q", launched.BlueprintPath, seed.Path)
	}
	reloaded, err := blueprintMgr.Get(ctx, run.ProjectID, source.Path)
	testutil.FailErr(t, "reload source", err)
	if reloaded.Content != source.Content {
		t.Fatal("source content must be unchanged")
	}
}

func TestLaunchFromBlueprintDeferStartArmsPendingPath(t *testing.T) {
	mgr, sessionID, projectDir := testWorkflowManager(t)
	ctx := context.Background()
	run, err := mgr.Starts.StartHuman(ctx, sessionID, api.StartWorkflowRunRequest{
		WorkflowID: "plan", WorkflowVersion: "1.0.0",
	})
	testutil.FailErr(t, "StartHuman", err)
	blueprintMgr, ok := mgr.Blueprints.Getter.(*blueprint.Manager)
	if !ok || blueprintMgr == nil {
		t.Fatal("expected *blueprint.Manager BlueprintGet")
	}
	source, err := blueprintMgr.Get(ctx, run.ProjectID, run.BlueprintPath)
	testutil.FailErr(t, "Get source", err)
	content := blueprint.ResetToDraftFrontmatter("# Deferred\n\n## Goal\nseed\n")
	source, err = blueprintMgr.Update(ctx, run.ProjectID, source.Path, &content, nil)
	testutil.FailErr(t, "Update source", err)

	target, err := workflowblueprints.ResolveLaunchTarget(source.Path, "plan", mgr.Resolver.Overlay.List())
	testutil.FailErr(t, "workflowblueprints.ResolveLaunchTarget", err)

	sess2, err := mgr.Verdicts.Sessions.Create(ctx, api.CreateSessionRequest{
		ProjectID: run.ProjectID,
		Posture:   api.SessionPostureSpec,
	}, run.ProjectID)
	testutil.FailErr(t, "Create session", err)
	ambient, err := mgr.Ambient.StartAmbient(ctx, sess2.ID, "implement", "1.0.0")
	testutil.FailErr(t, "StartAmbient", err)

	launched, seed, err := mgr.Blueprints.LaunchFromBlueprint(ctx, sess2.ID, source, target, blueprintMgr, projectDir, true)
	testutil.FailErr(t, "LaunchFromBlueprint defer", err)
	if launched != nil {
		t.Fatal("defer_start must not start a run")
	}
	if seed == nil || seed.Path == "" {
		t.Fatal("expected seed path")
	}

	started, err := mgr.Starts.StartHuman(ctx, sess2.ID, api.StartWorkflowRunRequest{
		WorkflowID: "plan", WorkflowVersion: "1.0.0",
	})
	testutil.FailErr(t, "StartHuman after defer", err)
	if started.BlueprintPath != seed.Path {
		t.Fatalf("started path = %q want seed %q", started.BlueprintPath, seed.Path)
	}
	ambient, err = mgr.Store.Runs.Get(ctx, ambient.ID)
	testutil.FailErr(t, "Get ambient", err)
	if ambient.Status != api.WorkflowRunStatusCanceled {
		t.Fatalf("ambient status = %q want canceled", ambient.Status)
	}
}
