package workflow

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/blueprint"
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestPlanStartMintsConventionPath(t *testing.T) {
	mgr, sessionID, _ := testWorkflowManager(t)
	ctx := context.Background()
	run, err := mgr.StartHuman(ctx, sessionID, api.StartWorkflowRunRequest{
		WorkflowID: "plan", WorkflowVersion: "1.0.0",
	})
	testutil.FailErr(t, "StartHuman plan", err)
	path := filepath.ToSlash(strings.TrimSpace(run.BlueprintPath))
	if path == "" {
		t.Fatal("expected minted blueprint path")
	}
	if path == settingsoverlay.DirName()+"/blueprints/blueprint.md" {
		t.Fatalf("plan must not force blueprint.md; got %q", path)
	}
	if err := blueprint.ValidateConventionPath(path); err != nil {
		t.Fatalf("path %q: %v", path, err)
	}
}

func TestOptionsStartUsesFixedBlueprintPath(t *testing.T) {
	mgr, sessionID, _ := testWorkflowManager(t)
	ctx := context.Background()
	run, err := mgr.StartHuman(ctx, sessionID, api.StartWorkflowRunRequest{
		WorkflowID: "options", WorkflowVersion: "1.0.0",
	})
	testutil.FailErr(t, "StartHuman options", err)
	want := settingsoverlay.Rel("blueprints/options-selection.md")
	if filepath.ToSlash(run.BlueprintPath) != want {
		t.Fatalf("BlueprintPath = %q want %q", run.BlueprintPath, want)
	}
}

func TestNotifyBlueprintPathWrittenAdvancesExpand(t *testing.T) {
	mgr, sessStore, blueprintMgr, _ := testManager(t)
	setTestRegistry(t, mgr, blueprintMgr, conditions.RegistryDeps{})
	ctx := context.Background()
	sess, err := sessStore.Create(ctx, api.CreateSessionRequest{
		Posture:   api.SessionPostureBuild,
		ProjectID: testdbseed.DefaultProjectID,
	}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "Create session", err)
	sessionID := sess.ID
	run, err := startPlanRun(ctx, mgr, sessionID)
	testutil.FailErr(t, "start plan", err)
	run = completePlanIntakeT(ctx, t, mgr, run)
	run, err = completePlanResearchAtDepthNone(ctx, mgr, run)
	testutil.FailErr(t, "complete research at depth none", err)
	if run.CurrentPhase != "expand" {
		t.Fatalf("phase = %q want expand", run.CurrentPhase)
	}
	seedValidPlanContent(t, blueprintMgr, run.BlueprintPath)
	mgr.NotifyBlueprintPathWritten(ctx, sessionID, "", run.BlueprintPath)
	run, err = mgr.Get(ctx, run.ID)
	testutil.FailErr(t, "Get after notify", err)
	if run.CurrentPhase == "expand" {
		t.Fatal("bound write must advance past expand")
	}
}

func TestNotifyRetargetsProvisionalPathOnce(t *testing.T) {
	mgr, sessStore, blueprintMgr, _ := testManager(t)
	setTestRegistry(t, mgr, blueprintMgr, conditions.RegistryDeps{})
	ctx := workflowCaller(t, mgr)
	sess, err := sessStore.Create(ctx, api.CreateSessionRequest{
		Posture:   api.SessionPostureBuild,
		ProjectID: testdbseed.DefaultProjectID,
	}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "Create session", err)
	run, err := mgr.StartHuman(ctx, sess.ID, api.StartWorkflowRunRequest{
		WorkflowID: "plan", WorkflowVersion: "1.0.0",
	})
	testutil.FailErr(t, "start plan", err)
	if !blueprint.IsProvisionalPath(run.BlueprintPath) {
		t.Fatalf("start path %q should be provisional", run.BlueprintPath)
	}
	run, err = mgr.ResolveUserFeedback(ctx, sess.ID, run.ID, workflowRequestFeedbackID, "retarget this blueprint")
	testutil.FailErr(t, "resolve workflow request", err)
	run, err = completePlanResearchAtDepthNone(ctx, mgr, run)
	testutil.FailErr(t, "complete research at depth none", err)
	from := run.BlueprintPath
	seedValidPlanContent(t, blueprintMgr, from)
	mgr.NotifyBlueprintPathWritten(ctx, sess.ID, "", from)
	run, err = mgr.Get(ctx, run.ID)
	testutil.FailErr(t, "Get after retarget", err)
	if run.BlueprintPath == from {
		t.Fatal("provisional path must retarget once a title is declared")
	}
	if blueprint.IsProvisionalPath(run.BlueprintPath) {
		t.Fatalf("retargeted path still provisional: %q", run.BlueprintPath)
	}
	if _, err := blueprintMgr.Get(ctx, run.ProjectID, run.BlueprintPath); err != nil {
		testutil.FailErr(t, "get retargeted file", err)
	}
	if _, err := blueprintMgr.Get(ctx, run.ProjectID, from); err == nil {
		t.Fatal("old provisional path must be gone")
	}
}

func TestNotifyBlueprintPathWrittenNonBoundNoop(t *testing.T) {
	mgr, sessionID, _ := testWorkflowManager(t)
	ctx := context.Background()
	run, err := startPlanRun(ctx, mgr, sessionID)
	testutil.FailErr(t, "start plan", err)
	run = completePlanIntakeT(ctx, t, mgr, run)
	run, err = completePlanResearchAtDepthNone(ctx, mgr, run)
	testutil.FailErr(t, "complete research at depth none", err)
	phase := run.CurrentPhase
	mgr.NotifyBlueprintPathWritten(ctx, sessionID, "", settingsoverlay.DirName()+"/blueprints/other.md")
	run, err = mgr.Get(ctx, run.ID)
	testutil.FailErr(t, "Get", err)
	if run.CurrentPhase != phase {
		t.Fatalf("non-bound write advanced phase %q → %q", phase, run.CurrentPhase)
	}
}

func TestStampHumanApprovalUsesRunBlueprintPath(t *testing.T) {
	cfg := &workflowdef.HumanApprovalConfig{Blueprint: settingsoverlay.Rel("blueprints/blueprint.md")}
	vars := StampHumanApprovalPhase(nil, cfg, settingsoverlay.DirName()+"/blueprints/weather-abcd1234.md")
	got, _ := DotPathString(vars, "human_approval.blueprint_path")
	if got != settingsoverlay.DirName()+"/blueprints/weather-abcd1234.md" {
		t.Fatalf("stamped path = %q", got)
	}
}

func TestMintBlueprintTitle(t *testing.T) {
	if got := mintBlueprintTitle("plan", "auth-fix"); got != "auth-fix" {
		t.Fatalf("got %q", got)
	}
	if got := mintBlueprintTitle("Ship auth", ""); got != "Ship auth" {
		t.Fatalf("got %q", got)
	}
	if got := mintBlueprintTitle("", ""); got != "blueprint" {
		t.Fatalf("got %q", got)
	}
	if got := mintBlueprintTitle("blueprint", "auth-fix"); got != "auth-fix" {
		t.Fatalf("got %q", got)
	}
}
