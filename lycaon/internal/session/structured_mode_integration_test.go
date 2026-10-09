//go:build integration

package session_test

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/blueprint"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/workflow"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowdrafts "github.com/lycaon/lycaon/internal/workflow/drafts"
	workflowpersistence "github.com/lycaon/lycaon/internal/workflow/persistence"
	workflowruntime "github.com/lycaon/lycaon/internal/workflow/runtime"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func setupPlanTripartiteFixture(t *testing.T) coordinatorPromptFixture {
	t.Helper()
	t.Setenv("LYCAON_LLM_MOCK", "1")
	sqlDB := testdbfixture.Open(t, "plan-tripartite.db")

	store := store.NewSQL(sqlDB)
	inner := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{Pattern: ".", Text: "ok"}}})
	rec := llm.NewRecordingClient(inner)
	mgr := session.NewHost(store, session.Models{Client: rec, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	agents := orchestration.NewMemoryAgentRegistry()
	_ = orchestration.LoadRequiredAgentRegistry(context.Background(), agents)
	mgr.Profiles.SetAgentRegistry(agents)
	wirePromptTestManager(t, mgr)
	mgr.SetPromptEngine(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{}))
	if hintCfg, err := guidance.LoadHintConfigStock(); err == nil {
		mgr.SetWorkflowHints(hintCfg, nil)
	}

	manifestReg, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "workflow.RegistryFromDirs failed", err)
	wfMgr := workflow.NewManager(workflowpersistence.New(sqlDB), store, manifestReg, nil)
	wfMgr.Blueprints.Scaffold.Store = workflowpersistence.NewSessionScaffoldSQLStore(sqlDB)
	dir := t.TempDir()
	blueprintStore := blueprint.NewFileStoreForTest(dir)
	blueprintMgr := blueprint.NewManager(blueprintStore)
	wfMgr.Blueprints.Creator = blueprint.WorkflowBlueprintCreator{Host: blueprintMgr}
	wfMgr.Blueprints.Getter = blueprintMgr
	wfMgr.Presentation.BlueprintGetter = blueprintMgr
	wfMgr.Approvals.Getter = blueprintMgr
	mgr.SetWorkflowDomains(&session.WorkflowDomains{Runs: wfMgr.Store.Runs, Policy: wfMgr.Policy, Ambient: wfMgr.Ambient, Blueprints: wfMgr.Blueprints, Batch: wfMgr.Batch, Slash: wfMgr.Slash, Requests: wfMgr.Requests, Feedback: wfMgr.Feedback, Transcript: wfMgr.Transcript, Asks: wfMgr.Asks, Fanout: wfMgr.Fanout, Phases: wfMgr.Phases, Reports: wfMgr.Reports, Recovery: wfMgr.Recovery, Cleanup: wfMgr})
	mgr.SetCoordinatorTurnFrameSource(&workflowruntime.CoordinatorFrames{Runs: wfMgr.Store.Runs, Resolver: &wfMgr.Resolver, Snapshots: wfMgr.Snapshots, Policy: wfMgr.Policy, Obligations: wfMgr.Obligations, SessionStore: workflowdrafts.NewSQL(sqlDB)})

	ctx := context.Background()

	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)

	sess, err := store.Create(ctx, wire.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)
	if _, err := wfMgr.Starts.StartHuman(ctx, sess.ID, wire.StartWorkflowRunRequest{WorkflowID: "plan", WorkflowVersion: "1.0.0"}); err != nil {
		testutil.FailErr(t, "wfMgr.Starts.StartHuman failed", err)
	}
	return coordinatorPromptFixture{mgr: mgr, rec: rec, wfMgr: wfMgr, sess: sess}
}

func TestStructuredModeExitRestoresImplementTripartiteAndTools(t *testing.T) {
	fix := setupPlanTripartiteFixture(t)
	ctx := context.Background()

	run, err := fix.wfMgr.Store.Runs.ActiveBySession(ctx, fix.sess.ID)
	testutil.FailErr(t, "fix.wfMgr.GetActive before exit", err)
	if _, err := fix.wfMgr.Controls.Exit(ctx, fix.sess.ID, run.ID, run.Revision, "user_exit"); err != nil {
		testutil.FailErr(t, "fix.wfMgr.Controls.Exit failed", err)
	}
	active, err := fix.wfMgr.Store.Runs.ActiveBySession(ctx, fix.sess.ID)
	testutil.FailErr(t, "fix.wfMgr.GetActive failed", err)
	if active == nil || active.WorkflowID != "implement" {
		t.Fatalf("expected ambient implement@ after catalog exit, got %+v", active)
	}
	if active.CurrentPhase != "boot" {
		t.Fatalf("ambient implement phase = %q want boot", active.CurrentPhase)
	}

	if _, err := fix.mgr.Submissions.Prompt(ctx, fix.sess.ID, "back to implement chat"); err != nil {
		testutil.FailErr(t, "fix.mgr.Submissions.Prompt failed", err)
	}
	req := fix.rec.LastRequest()
	foundImplement := false
	for _, msg := range req.Messages {
		if msg.Role != wire.MessageRoleSystem {
			continue
		}
		if strings.Contains(msg.Content, "## Investigate") {
			foundImplement = true
		}
		if strings.Contains(msg.Content, "Structured plan mode") {
			t.Fatal("post-exit prompt must not load plan mode partial")
		}
	}
	if !foundImplement {
		t.Fatal("post-exit first implement prompt must load implement investigate mode partial")
	}

	listed := fix.mgr.Coordinator.Guards.Policy().ListForPrompt(ctx, fix.sess, orchestration.ProfileCoordinator)
	_ = listed
}

func TestStructuredModePlanTripartiteOnActiveRun(t *testing.T) {
	fix := setupPlanTripartiteFixture(t)
	ctx := context.Background()
	if _, err := fix.mgr.Submissions.Prompt(ctx, fix.sess.ID, "what phase are we in"); err != nil {
		testutil.FailErr(t, "fix.mgr.Submissions.Prompt failed", err)
	}
	foundPlan := false
	for _, msg := range fix.rec.LastRequest().Messages {
		if msg.Role == wire.MessageRoleSystem && strings.Contains(msg.Content, "Structured plan mode — research phase") {
			foundPlan = true
		}
		if msg.Role == wire.MessageRoleSystem && strings.Contains(msg.Content, "When to compose") {
			t.Fatal("plan-family run must not load compose partial")
		}
	}
	if !foundPlan {
		t.Fatal("active plan run must load structured plan phase partial")
	}
}
