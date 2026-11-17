package wiring

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/blueprint"
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/guidance/feedback"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workflow"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowdrafts "github.com/lycaon/lycaon/internal/workflow/drafts"
	workflowinputs "github.com/lycaon/lycaon/internal/workflow/inputs"
	workflowpersistence "github.com/lycaon/lycaon/internal/workflow/persistence"
	workflowruntime "github.com/lycaon/lycaon/internal/workflow/runtime"
	"github.com/lycaon/lycaon/pkg/api"
)

// A blocked bugbash advance reaches the coordinator inject with its workflow,
// phase and failing gate ids.
func TestDefaultPipelineActiveWorkflowInjectVisible(t *testing.T) {
	ctx := context.Background()
	sqlDB := testdbfixture.Open(t, "pipeline-inject.db")

	sessions := store.NewSQL(sqlDB)
	mgr := session.NewHost(sessions, session.Models{Limits: settings.DefaultSessionLimits()}, nil)
	agents := orchestration.NewMemoryAgentRegistry()
	testutil.FailErr(t, "LoadRequiredAgentRegistry", orchestration.LoadRequiredAgentRegistry(ctx, agents))
	mgr.Profiles.SetAgentRegistry(agents)
	mgr.SetPromptEngine(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: configlayout.FindModuleRoot()}))
	hintCfg, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "LoadHintConfigStock", err)
	gateCfg, err := feedback.LoadGateFeedbackCatalog()
	testutil.FailErr(t, "LoadGateFeedbackCatalog", err)
	mgr.SetWorkflowHints(hintCfg, gateCfg)

	sessionWF := workflowdrafts.NewSQL(sqlDB)
	manifestReg, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "workflow.RegistryFromDirs failed", err)
	wfMgr := workflow.NewManager(workflowpersistence.New(sqlDB), sessions, manifestReg, nil)
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	testutil.FailErr(t, "conditions.NewDefaultRegistry failed", err)
	wfMgr.SetConditionRegistry(reg)
	wfMgr.Resolver.SessionStore = sessionWF
	mgr.SetWorkflowDomains(&session.WorkflowDomains{Runs: wfMgr.Store.Runs, Policy: wfMgr.Policy, Ambient: wfMgr.Ambient, Blueprints: wfMgr.Blueprints, Batch: wfMgr.Batch, Slash: wfMgr.Slash, Requests: wfMgr.Requests, Feedback: wfMgr.Feedback, Transcript: wfMgr.Transcript, Asks: wfMgr.Asks, Fanout: wfMgr.Fanout, Phases: wfMgr.Phases, Reports: wfMgr.Reports, Recovery: wfMgr.Recovery, Cleanup: wfMgr})
	frameLoader := &workflowruntime.CoordinatorFrames{Runs: wfMgr.Store.Runs, Resolver: &wfMgr.Resolver, Snapshots: wfMgr.Snapshots, Policy: wfMgr.Policy, Obligations: wfMgr.Obligations, SessionStore: sessionWF}
	mgr.SetCoordinatorTurnFrameSource(frameLoader)

	dir := t.TempDir()
	blueprintMgr := blueprint.NewManager(blueprint.NewFileStoreForTest(dir))
	wfMgr.Blueprints.Creator = blueprint.WorkflowBlueprintCreator{Manager: blueprintMgr}
	wfMgr.Blueprints.Getter = blueprintMgr
	wfMgr.Presentation.BlueprintGetter = blueprintMgr
	wfMgr.Approvals.Getter = blueprintMgr
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)

	sess, err := sessions.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "store.Create failed", err)
	_, err = wfMgr.Starts.StartHuman(ctx, sess.ID, api.StartWorkflowRunRequest{WorkflowID: "bugbash", WorkflowVersion: "1.1.0"})
	testutil.FailErr(t, "wfMgr.Starts.StartHuman failed", err)
	run, err := wfMgr.Store.Runs.ActiveBySession(ctx, sess.ID)
	if err != nil || run == nil {
		t.Fatalf("active run = %+v err = %v", run, err)
	}
	if _, err := wfMgr.Phases.Advance(ctx, run.ID); err == nil {
		t.Fatal("expected advance blocked before parallel hunt stages complete")
	}

	runCtx, err := mgr.Coordinator.Context.RunContext(ctx, sess.ID)
	testutil.FailErr(t, "mgr.CoordinatorRunContext failed", err)
	frame, block := renderWorkflowFrame(t, ctx, frameLoader, sess.ID, surface.StaticWorkflowHintCodes(runCtx, false))
	if frame.RunContext.WorkflowID != "bugbash" || frame.RunContext.CurrentPhase != "hunt" || !slices.Contains(frame.RunContext.FailedLeaves, "parallel_stages_complete") {
		t.Fatalf("turn frame run context = %+v", frame.RunContext)
	}
	for _, want := range []string{inject.ActiveWorkflowInjectSentinel, "bugbash", "hunt", "parallel_stages_complete"} {
		if !strings.Contains(block, want) {
			t.Fatalf("missing %q in:\n%s", want, block)
		}
	}
}

// On the assembled host, a pending workflow question reaches the coordinator as
// the feedback-pending kick while workflow_user_feedback stays offered, and the
// kick clears once the question is answered.
func TestPendingWorkflowFeedbackReachesCoordinator(t *testing.T) {
	h := BuildForTest(t, WithoutCoordinatorLoop())
	ctx := h.OwnerCtx(t, t.Context())
	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{}, t.TempDir())
	testutil.FailErr(t, "create session", err)
	AttachDefaultAmbient(t, h, ctx, sess.ID)
	pendingKick := anchor.InformRender(anchor.FeedbackPending)
	if slices.Contains(h.Sessions.Manager.Coordinator.Guidance.PendingIDs(ctx, sess.ID), pendingKick) {
		t.Fatal("feedback-pending kick queued before any question")
	}

	handle, err := h.Workflows.Manager.Asks.RequestUserInput(ctx, sess.ID, workflowinputs.UserInputRequest{
		Prompt: "Which platform?", ResponseType: workflowdef.FeedbackResponseText, ToolCallID: "call-platform",
	})
	testutil.FailErr(t, "open workflow question", err)
	if ids := h.Sessions.Manager.Coordinator.Guidance.PendingIDs(ctx, sess.ID); !slices.Contains(ids, pendingKick) {
		t.Fatalf("pending kicks = %v want %s", ids, pendingKick)
	}
	current, err := h.Store.Get(ctx, sess.ID)
	testutil.FailErr(t, "reload session", err)
	offered := false
	for _, meta := range h.Sessions.Manager.Coordinator.Guards.Policy().ListForPrompt(ctx, current, orchestration.ProfileCoordinator) {
		offered = offered || meta.Name == "workflow_user_feedback"
	}
	if !offered {
		t.Fatal("workflow_user_feedback not offered while a question is pending")
	}

	_, err = h.Workflows.Manager.Feedback.ResolveUserFeedback(ctx, sess.ID, handle.RunID, handle.PhaseID, "macOS")
	testutil.FailErr(t, "answer workflow question", err)
	if ids := h.Sessions.Manager.Coordinator.Guidance.PendingIDs(ctx, sess.ID); slices.Contains(ids, pendingKick) {
		t.Fatalf("answered question left %s pending: %v", pendingKick, ids)
	}
}
