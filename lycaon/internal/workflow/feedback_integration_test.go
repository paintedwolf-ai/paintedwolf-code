//go:build integration

package workflow_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/workflow"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func feedbackFlowManifest() workflowdef.Manifest {
	return workflowdef.FinalizeManifest(workflowdef.Manifest{
		ID:      "feedback-flow",
		Version: "1.0.0",
		Controls: workflowdef.ManifestControls{
			PhaseAdvance: workflowdef.PhaseAdvanceHost,
		},
		PhaseDefs: []workflowdef.PhaseDef{{
			ID:           "clarify",
			CompleteWhen: "user_feedback_received:clarify",
			Next:         "done",
			OnEnter: workflowdef.PhaseOnEnter{
				RequestUserFeedback: &workflowdef.UserFeedbackPrompt{Prompt: "REST or GraphQL?"},
			},
		}, {ID: "done"}},
	})
}

func setupFeedbackIntegration(t *testing.T) (*workflow.RunManager, *session.Manager, *wire.Session, context.Context) {
	t.Helper()
	sqlDB := testdbfixture.Open(t, "feedback-int.db")

	store := store.NewSQL(sqlDB)
	sessMgr := session.NewManager(store, llm.NewMockProvider(nil), tools.NewStubRegistry(), settings.DefaultSessionLimits())
	testutil.FailErr(t, "install anchor registry", sessMgr.InstallAnchorRegistry())

	manifest := feedbackFlowManifest()
	manifestReg := workflowdef.NewRegistry(map[string]workflowdef.Manifest{
		manifest.ID + "@" + manifest.Version: manifest,
	})
	wfMgr := workflow.NewManager(workflow.NewSQLStore(sqlDB), store, manifestReg, nil)
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	testutil.FailErr(t, "build conditions registry", err)
	wfMgr.SetConditionRegistry(reg)
	wfMgr.OnFeedbackPending = func(_ context.Context, sessionID, _ string) {
		sessMgr.Emit(context.Background(), sessionID, anchor.FeedbackPending, anchor.Envelope{})
	}

	dir := t.TempDir()

	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)

	sess, err := store.Create(context.Background(), wire.CreateSessionRequest{
		Posture: wire.SessionPostureSpec,
	}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)
	sessMgr.SetWorkflowSessionView(wfMgr, workflow.PolicySource(wfMgr))
	return wfMgr, sessMgr, sess, testdbseed.OwnerCaller(t, context.Background(), sqlDB)
}

func TestClarifyTemplateChatSatisfies(t *testing.T) {
	wfMgr, _, sess, _ := setupFeedbackIntegration(t)
	ctx := context.Background()
	run, err := wfMgr.StartHuman(ctx, sess.ID, wire.StartWorkflowRunRequest{
		WorkflowID: "feedback-flow", WorkflowVersion: "1.0.0",
	})
	testutil.FailErr(t, "wfMgr.StartHuman failed", err)
	if run.CurrentPhase != "clarify" {
		t.Fatalf("phase = %q", run.CurrentPhase)
	}
	if err := wfMgr.TryResolveUserFeedback(ctx, sess.ID, "", testutil.HostOwner().ID, "Use GraphQL"); err != nil {
		testutil.FailErr(t, "wfMgr.TryResolveUserFeedback failed", err)
	}
	run, err = wfMgr.Get(ctx, run.ID)
	testutil.FailErr(t, "wfMgr.Get failed", err)
	if run.CurrentPhase != "done" {
		t.Fatalf("phase = %q want done after chat feedback", run.CurrentPhase)
	}
}

func TestClarifyTemplateHTTPSatisfies(t *testing.T) {
	wfMgr, _, sess, ctx := setupFeedbackIntegration(t)
	run, err := wfMgr.StartHuman(ctx, sess.ID, wire.StartWorkflowRunRequest{
		WorkflowID: "feedback-flow", WorkflowVersion: "1.0.0",
	})
	testutil.FailErr(t, "wfMgr.StartHuman failed", err)
	run, err = wfMgr.ResolveUserFeedback(ctx, sess.ID, run.ID, "clarify", "REST please")
	testutil.FailErr(t, "wfMgr.ResolveUserFeedback failed", err)
	if run.CurrentPhase != "done" {
		t.Fatalf("phase = %q want done after HTTP feedback", run.CurrentPhase)
	}
}

func TestFeedbackKickQueuedOnPhaseEntry(t *testing.T) {
	root := configlayout.FindModuleRoot()
	sqlDB := testdbfixture.Open(t, "feedback-kick.db")

	store := store.NewSQL(sqlDB)
	sessMgr := session.NewManager(store, llm.NewMockProvider(nil), tools.NewStubRegistry(), settings.DefaultSessionLimits())
	testutil.FailErr(t, "install anchor registry", sessMgr.InstallAnchorRegistry())

	manifest := feedbackFlowManifest()
	wfMgr := workflow.NewManager(workflow.NewSQLStore(sqlDB), store, workflowdef.NewRegistry(map[string]workflowdef.Manifest{
		manifest.ID + "@" + manifest.Version: manifest,
	}), nil)
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	testutil.FailErr(t, "build conditions registry", err)
	wfMgr.SetConditionRegistry(reg)
	kicked := false
	wfMgr.OnFeedbackPending = func(_ context.Context, sessionID, phaseID string) {
		kicked = true
		if phaseID != "clarify" {
			t.Fatalf("phase = %q want clarify", phaseID)
		}
		sessMgr.Emit(context.Background(), sessionID, anchor.FeedbackPending, anchor.Envelope{})
	}

	dir := t.TempDir()

	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)

	sess, err := store.Create(context.Background(), wire.CreateSessionRequest{
		Posture: wire.SessionPostureSpec,
	}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)
	if _, err := wfMgr.StartHuman(context.Background(), sess.ID, wire.StartWorkflowRunRequest{
		WorkflowID: "feedback-flow", WorkflowVersion: "1.0.0",
	}); err != nil {
		t.Fatal(err)
	}
	if !kicked {
		t.Fatal("OnFeedbackPending hook not invoked")
	}
	kickPath := filepath.Join(root, "config", "packs", "painted-wolf", "platform", "guidance", "coordinator-feedback-pending.md")
	data, err := os.ReadFile(kickPath)
	testutil.FailErr(t, "read file", err)
	if !strings.Contains(string(data), "pending_feedback") {
		t.Fatalf("kick template = %q", data)
	}
}

func TestClarifyThenImplementTemplateOverridesPlanStub(t *testing.T) {
	templates, err := workflow.LoadTemplatesFromDir(extpacks.Bundled(config.PlatformFlows.Join("_templates")))
	testutil.FailErr(t, "load workflow templates", err)
	tpl, ok := templates["clarify-then-implement-template"]
	if !ok {
		t.Fatal("missing clarify-then-implement-template")
	}
	if !strings.Contains(tpl.ManifestRaw, "id: research") {
		t.Fatalf("template manifest = %s", tpl.ManifestRaw)
	}
	if !strings.Contains(tpl.ManifestRaw, "user_feedback_received:clarify") {
		t.Fatal("template gate must reference clarify phase id")
	}
}
