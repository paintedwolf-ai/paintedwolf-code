//go:build integration

package session_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/blueprint"
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/batch"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	loopwake "github.com/lycaon/lycaon/internal/coordinator/loopwake"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/workflow"
	workflowcomposition "github.com/lycaon/lycaon/internal/workflow/composition"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowdrafts "github.com/lycaon/lycaon/internal/workflow/drafts"
	workflowpersistence "github.com/lycaon/lycaon/internal/workflow/persistence"
	workflowruntime "github.com/lycaon/lycaon/internal/workflow/runtime"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestMaybeAdvanceCoordinatorBatchOnTaskEnqueued_writerStillDispatches(t *testing.T) {
	root := configlayout.FindModuleRoot()
	sqlDB := testdbfixture.Open(t, "batch-writer.db")

	store := store.NewSQL(sqlDB)
	mgr := session.NewHost(store, session.Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	agents := orchestration.NewMemoryAgentRegistry()
	testutil.FailErr(t, "LoadRequiredAgentRegistry", orchestration.LoadRequiredAgentRegistry(context.Background(), agents))
	mgr.Profiles.SetAgentRegistry(agents)

	wfStore := workflowpersistence.New(sqlDB)
	bundledDir := filepath.Join(root, "config", "packs", "painted-wolf", "platform", "workflows")
	manifestRegistry, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "RegistryFromDirs", err)
	wfMgr := workflow.NewManager(wfStore, store, manifestRegistry, nil)
	mgr.SetWorkflowDomains(&session.WorkflowDomains{Runs: wfMgr.Store.Runs, Policy: wfMgr.Policy, Ambient: wfMgr.Ambient, Blueprints: wfMgr.Blueprints, Batch: wfMgr.Batch, Slash: wfMgr.Slash, Requests: wfMgr.Requests, Feedback: wfMgr.Feedback, Transcript: wfMgr.Transcript, Asks: wfMgr.Asks, Fanout: wfMgr.Fanout, Phases: wfMgr.Phases, Reports: wfMgr.Reports, Recovery: wfMgr.Recovery, Cleanup: wfMgr})

	ctx := context.Background()
	projectDir := t.TempDir()
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, projectDir)
	sess, err := store.Create(ctx, wire.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	ref, err := workflowdef.LoadRegistryConfig(extpacks.OnDisk(bundledDir))
	testutil.FailErr(t, "LoadRegistryConfig", err)
	_, err = wfMgr.Ambient.StartAmbient(ctx, sess.ID, ref.ID, ref.Version)
	testutil.FailErr(t, "StartAmbient", err)

	mgr.Coordinator.Batch.TaskEnqueued(ctx, sess.ID, orchestration.ProfileImplementer)

	state := mgr.Workers.State.ForSession(ctx, sess)
	if state.BatchPhase != batch.PhaseDispatch {
		t.Fatalf("batch phase = %q want dispatch after writer enqueue", state.BatchPhase)
	}
}

func TestFinishPromptExecutionQueuesOverlayIntegrateCompleteKick(t *testing.T) {
	root := configlayout.FindModuleRoot()
	sqlDB := testdbfixture.Open(t, "overlay-integrate-kick.db")

	store := store.NewSQL(sqlDB)
	mgr := session.NewHost(store, session.Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	agents := orchestration.NewMemoryAgentRegistry()
	testutil.FailErr(t, "LoadRequiredAgentRegistry", orchestration.LoadRequiredAgentRegistry(context.Background(), agents))
	mgr.Profiles.SetAgentRegistry(agents)
	wirePromptTestManager(t, mgr)
	mgr.SetPromptEngine(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{}))
	testutil.FailErr(t, "install anchor registry", mgr.Coordinator.Guidance.InstallAnchorRegistry())

	wfStore := workflowpersistence.New(sqlDB)
	bundledDir := filepath.Join(root, "config", "packs", "painted-wolf", "platform", "workflows")
	manifestRegistry, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "RegistryFromDirs", err)
	wfMgr := workflow.NewManager(wfStore, store, manifestRegistry, nil)
	mgr.SetWorkflowDomains(&session.WorkflowDomains{Runs: wfMgr.Store.Runs, Policy: wfMgr.Policy, Ambient: wfMgr.Ambient, Blueprints: wfMgr.Blueprints, Batch: wfMgr.Batch, Slash: wfMgr.Slash, Requests: wfMgr.Requests, Feedback: wfMgr.Feedback, Transcript: wfMgr.Transcript, Asks: wfMgr.Asks, Fanout: wfMgr.Fanout, Phases: wfMgr.Phases, Reports: wfMgr.Reports, Recovery: wfMgr.Recovery, Cleanup: wfMgr})
	mgr.SetLoopWorkflowSource(&loopwake.WorkflowDomains{Runs: wfMgr.Store.Runs, Approvals: wfMgr.Policy, Obligations: wfMgr.Obligations})
	prog := progress.NewMemoryStore()
	mgr.SetProgressStore(prog)

	ctx := context.Background()
	projectDir := t.TempDir()
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, projectDir)
	sess, err := store.Create(ctx, wire.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)
	ref, err := workflowdef.LoadRegistryConfig(extpacks.OnDisk(bundledDir))
	testutil.FailErr(t, "LoadRegistryConfig", err)
	_, err = wfMgr.Ambient.StartAmbient(ctx, sess.ID, ref.ID, ref.Version)
	testutil.FailErr(t, "StartAmbient", err)
	prog.Set(sess.ID, "## Progress\n- [x] implement\n- [x] verify")

	testutil.FailErr(t, "ApplyCoordinatorBatchEvent dispatch", wfMgr.Batch.ApplyCoordinatorBatchEvent(ctx, sess.ID, batch.EventWriterTaskEnqueued, 0))
	testutil.FailErr(t, "ApplyCoordinatorBatchEvent integrate", wfMgr.Batch.ApplyCoordinatorBatchEvent(ctx, sess.ID, batch.EventOverlaysPendingIdle, 0))

	func() {
		_ = mgr.Runner.Settlement.Finish(ctx, sess.ID, false, true, "")
		_ = mgr.Runner.Settlement.Drain(ctx, sess.ID)
	}()

	kickID, ok := mgr.Runner.Coordinator.Kicks().PeekPendingKickID(sess.ID)
	wantID := anchor.InformRender(anchor.OverlayPromoteComplete)
	if !ok || kickID != wantID {
		t.Fatalf("kick_id = %q ok=%v want %q", kickID, ok, wantID)
	}
}

func TestReconcileCoordinatorBatch_skipsSynthesisReadyWithoutBatchReady(t *testing.T) {
	root := configlayout.FindModuleRoot()
	sqlDB := testdbfixture.Open(t, "batch-reconcile-wrapup.db")

	store := store.NewSQL(sqlDB)
	mgr := session.NewHost(store, session.Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	agents := orchestration.NewMemoryAgentRegistry()
	testutil.FailErr(t, "LoadRequiredAgentRegistry", orchestration.LoadRequiredAgentRegistry(context.Background(), agents))
	mgr.Profiles.SetAgentRegistry(agents)

	wfStore := workflowpersistence.New(sqlDB)
	bundledDir := filepath.Join(root, "config", "packs", "painted-wolf", "platform", "workflows")
	manifestRegistry, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "RegistryFromDirs", err)
	wfMgr := workflow.NewManager(wfStore, store, manifestRegistry, nil)
	mgr.SetWorkflowDomains(&session.WorkflowDomains{Runs: wfMgr.Store.Runs, Policy: wfMgr.Policy, Ambient: wfMgr.Ambient, Blueprints: wfMgr.Blueprints, Batch: wfMgr.Batch, Slash: wfMgr.Slash, Requests: wfMgr.Requests, Feedback: wfMgr.Feedback, Transcript: wfMgr.Transcript, Asks: wfMgr.Asks, Fanout: wfMgr.Fanout, Phases: wfMgr.Phases, Reports: wfMgr.Reports, Recovery: wfMgr.Recovery, Cleanup: wfMgr})

	ctx := context.Background()
	projectDir := t.TempDir()
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, projectDir)
	sess, err := store.Create(ctx, wire.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	ref, err := workflowdef.LoadRegistryConfig(extpacks.OnDisk(bundledDir))
	testutil.FailErr(t, "LoadRegistryConfig", err)
	_, err = wfMgr.Ambient.StartAmbient(ctx, sess.ID, ref.ID, ref.Version)
	testutil.FailErr(t, "StartAmbient", err)

	testutil.FailErr(t, "ApplyCoordinatorBatchEvent dispatch", wfMgr.Batch.ApplyCoordinatorBatchEvent(ctx, sess.ID, batch.EventWriterTaskEnqueued, 0))
	testutil.FailErr(t, "ApplyCoordinatorBatchEvent integrate", wfMgr.Batch.ApplyCoordinatorBatchEvent(ctx, sess.ID, batch.EventOverlaysPendingIdle, 0))

	inlineEdit := []wire.Message{
		{Role: wire.MessageRoleUser, Content: "fix handler"},
		{
			Role:      wire.MessageRoleAssistant,
			ToolCalls: []wire.ToolCall{{ID: "c1", Name: "edit", Args: map[string]any{"path": "internal/handler.go"}}},
		},
		{
			Role:    wire.MessageRoleTool,
			Content: "ok",
			ToolResult: &wire.ToolResult{
				Outcome:  wire.ToolResultOutcomeCompleted,
				FileEdit: &wire.FileEditSnapshot{Path: "internal/handler.go"},
			},
		},
	}
	testutil.FailErr(t, "append messages", store.AppendMessages(ctx, sess.ID, inlineEdit...))

	mgr.Coordinator.Batch.Reconcile(ctx, sess.ID)

	state := mgr.Workers.State.ForSession(ctx, sess)
	if state.BatchPhase == batch.PhaseSynthesize {
		t.Fatalf("batch phase = %q want not synthesize without verify pass", state.BatchPhase)
	}
	if state.BatchReadyForSynthesis {
		t.Fatal("BuildImplementSessionState must not mark batch ready before verifier pass")
	}
	if !state.WrapupGatesLoaded {
		t.Fatal("expected wrapup gates loaded on session state")
	}
}

type coordinatorPromptFixture struct {
	mgr   *session.Host
	rec   *llm.RecordingClient
	wfMgr *workflow.RunManager
	sess  *wire.Session
	brief string
}

func setupCoordinatorPromptFixture(t *testing.T) coordinatorPromptFixture {
	t.Helper()
	t.Setenv("LYCAON_LLM_MOCK", "1")
	root := configlayout.FindModuleRoot()
	sqlDB := testdbfixture.Open(t, "tripartite-prompt.db")

	store := store.NewSQL(sqlDB)
	inner := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{Pattern: ".", Text: "ok"}}})
	rec := llm.NewRecordingClient(inner)
	mgr := session.NewHost(store, session.Models{Client: rec, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	agents := orchestration.NewMemoryAgentRegistry()
	testutil.FailErr(t, "load agent registry", orchestration.LoadRequiredAgentRegistry(context.Background(), agents))
	mgr.Profiles.SetAgentRegistry(agents)
	wirePromptTestManager(t, mgr)
	mgr.SetPromptEngine(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{}))

	sessionWF := workflowdrafts.NewSQL(sqlDB)
	condReg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	testutil.FailErr(t, "build conditions registry", err)
	policy, err := workflowcomposition.LoadComposePolicy()
	testutil.FailErr(t, "workflowcomposition.LoadComposePolicy failed", err)
	composer := &workflowcomposition.Composer{
		SessionStore: sessionWF, Registry: condReg, Agents: agents, Policy: policy,
	}
	manifestReg, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "workflow.RegistryFromDirs failed", err)
	wfMgr := workflow.NewManager(workflowpersistence.New(sqlDB), store, manifestReg, nil)
	wfMgr.Resolver.SessionStore = sessionWF
	wfMgr.Blueprints.Scaffold.Store = workflowpersistence.NewSessionScaffoldSQLStore(sqlDB)
	dir := t.TempDir()
	blueprintStore := blueprint.NewFileStoreForTest(dir)
	blueprintMgr := blueprint.NewManager(blueprintStore)
	wfMgr.Blueprints.Creator = blueprint.WorkflowBlueprintCreator{Manager: blueprintMgr}
	wfMgr.Blueprints.Getter = blueprintMgr
	wfMgr.Presentation.BlueprintGetter = blueprintMgr
	wfMgr.Approvals.Getter = blueprintMgr
	mgr.SetWorkflowDomains(&session.WorkflowDomains{Runs: wfMgr.Store.Runs, Policy: wfMgr.Policy, Ambient: wfMgr.Ambient, Blueprints: wfMgr.Blueprints, Batch: wfMgr.Batch, Slash: wfMgr.Slash, Requests: wfMgr.Requests, Feedback: wfMgr.Feedback, Transcript: wfMgr.Transcript, Asks: wfMgr.Asks, Fanout: wfMgr.Fanout, Phases: wfMgr.Phases, Reports: wfMgr.Reports, Recovery: wfMgr.Recovery, Cleanup: wfMgr})
	mgr.SetCoordinatorTurnFrameSource(&workflowruntime.CoordinatorFrames{Runs: wfMgr.Store.Runs, Resolver: &wfMgr.Resolver, Snapshots: wfMgr.Snapshots, Policy: wfMgr.Policy, Obligations: wfMgr.Obligations, SessionStore: sessionWF, ConfigRoot: root})

	ctx := context.Background()

	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)

	sess, err := store.Create(ctx, wire.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)
	manifest := `id: hotfix-session
version: 1.0.0
extends: plan@1.0.0
phases:
  - id: research
    activity_label: Gathering requirements
    next: build
  - id: build
    activity_label: Implementing the change
    on_enter:
      set_posture: build
    complete_when: delegation_closeout_complete
`
	result, err := composer.Compose(ctx, workflowcomposition.ComposeRequest{
		SessionID:      sess.ID,
		ManifestYAML:   []byte(manifest),
		SessionPosture: sess.Posture,
		CreatedBy:      workflowdrafts.Coordinator,
	})
	testutil.FailErr(t, "compose workflow manifest", err)
	if _, err := wfMgr.Starts.StartHuman(ctx, sess.ID, wire.StartWorkflowRunRequest{WorkflowID: "hotfix-session", WorkflowVersion: "1.0.0", Request: "test request"}); err != nil {
		testutil.FailErr(t, "wfMgr.Starts.StartHuman failed", err)
	}
	return coordinatorPromptFixture{mgr: mgr, rec: rec, wfMgr: wfMgr, sess: sess, brief: result.EffectiveSummary.CoordinatorBrief}
}

func firstCoordinatorPromptRequest(t *testing.T, fix coordinatorPromptFixture, prompt string) modelcall.CompletionRequest {
	t.Helper()
	before := len(fix.rec.AllRequests())
	_, err := fix.mgr.Submissions.Prompt(t.Context(), fix.sess.ID, prompt)
	requireWorkflowCloseoutRejection(t, err)
	requests := fix.rec.AllRequests()
	if len(requests) <= before {
		t.Fatal("coordinator prompt made no model request")
	}
	return requests[before]
}

func TestPromptPrependsCoordinatorBriefOnSecondTurn(t *testing.T) {
	fix := setupCoordinatorPromptFixture(t)
	for _, prompt := range []string{"what is our workflow plan", "review the current workflow again"} {
		req := firstCoordinatorPromptRequest(t, fix, prompt)
		found := false
		for _, msg := range req.Messages {
			if msg.Role != wire.MessageRoleSystem || !strings.Contains(msg.Content, inject.ActiveWorkflowInjectSentinel) {
				continue
			}
			found = true
			if !strings.Contains(msg.Content, fix.brief) {
				t.Fatalf("active workflow inject lost composed brief %q: %s", fix.brief, msg.Content)
			}
		}
		if !found {
			t.Fatal("first model request of user turn omitted active workflow context")
		}
	}
}

func TestPromptIncludesFailedLeavesAfterAdvance409(t *testing.T) {
	fix := setupCoordinatorPromptFixture(t)
	ctx := context.Background()
	run, err := fix.wfMgr.Store.Runs.ActiveBySession(ctx, fix.sess.ID)
	if err != nil || run == nil {
		t.Fatal("missing active run")
	}
	if _, err := fix.wfMgr.Phases.Advance(ctx, run.ID); err == nil {
		t.Fatal("expected phase gate error")
	}
	req := firstCoordinatorPromptRequest(t, fix, "why blocked")
	for _, msg := range req.Messages {
		if msg.Role == wire.MessageRoleSystem && strings.Contains(msg.Content, "failed_leaves") {
			return
		}
	}
	t.Fatal("expected failed_leaves in coordinator context block")
}
