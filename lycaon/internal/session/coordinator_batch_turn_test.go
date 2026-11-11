package session_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/batch"
	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/oartest"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCoordinatorBatchTurn_oneSynthesisPerTurnLatch(t *testing.T) {
	hints, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "load hints", err)
	store := store.NewMemory()
	mgr := session.NewHost(store, session.Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	mgr.SetRejectFormatter(guidance.NewStaticRejectFormatter(hints))

	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{ProjectID: "coordinator"}, "coordinator")
	testutil.FailErr(t, "create session", err)

	mgr.Runner.Settlement.Begin(sess.ID, "")
	mgr.Coordinator.Batch.AcceptSynthesis(ctx, sess.ID)

	reject, block := guard.FormatHostNoToolTurnReject(
		sess,
		[]api.Message{{Role: api.MessageRoleUser, Origin: api.MessageOriginHost, Visibility: api.MessageVisibilityInternal, Kind: api.MessageKindHostLoopWake}},
		"Another report in the same turn.",
		nil,
		"implement_synthesis",
		true,
		surface.ImplementSessionState{BatchPhase: batch.PhaseSynthesize},
		mgr.Coordinator.Guards.Rejects,
		mgr.Coordinator.Batch.TurnGuard(sess.ID),
	)
	if !block || !strings.Contains(reject, guard.CoordinatorBatchAlreadyClosedCode) {
		t.Fatalf("reject=%q block=%v want latch block before scaffold reads closed", reject, block)
	}
}

func TestCoordinatorBatchTurn_groundingRetryDoesNotSetLatch(t *testing.T) {
	store := store.NewMemory()
	mgr := session.NewHost(store, session.Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())

	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{ProjectID: "coordinator"}, "coordinator")
	testutil.FailErr(t, "create session", err)

	mgr.Runner.Settlement.Begin(sess.ID, "")
	if mgr.Coordinator.Batch.TurnGuard(sess.ID).SynthesisAcceptedThisTurn {
		t.Fatal("fresh turn should not have synthesis latch")
	}
}

type executionCheckpoints struct{ pending []api.CheckpointEvent }

func (c *executionCheckpoints) ListPending(_ context.Context, id string, _ *api.CheckpointKind) ([]api.CheckpointEvent, error) {
	var result []api.CheckpointEvent
	for _, p := range c.pending {
		if p.SessionID == id {
			result = append(result, p)
		}
	}
	return result, nil
}

func TestExecutionObservationUsesApplicationOwners(t *testing.T) {
	for _, backend := range []string{"memory", "sql"} {
		t.Run(backend, func(t *testing.T) {
			var st session.Store = store.NewMemory()
			if backend == "sql" {
				database := testdbfixture.Open(t, "execution.db")
				testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, t.TempDir())
				st = store.NewSQL(database)
			}
			parent, err := st.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
			testutil.FailErr(t, "create root", err)
			child, err := st.CreateChild(t.Context(), parent, api.SpawnChildRequest{AgentType: "implementer"})
			testutil.FailErr(t, "create child", err)
			_, _, err = st.PutPromptSubmission(t.Context(), store.PromptSubmission{ID: "admission", SessionID: parent.ID, ProjectID: parent.ProjectID, Origin: store.PromptSubmissionOriginUser, SubmittedBy: parent.OwnerPersonID, InputDigest: "task", InputJSON: "{}"})
			testutil.FailErr(t, "admit task", err)
			manager := session.NewHost(st, session.Models{Client: nil, Provider: nil, Limits: settings.SessionLimits{}, Cost: nil}, nil)
			checkpoints := &executionCheckpoints{}
			manager.Observations.SetExecutionCheckpoints(checkpoints)
			check := func(want bool) {
				t.Helper()
				observation, err := manager.Observations.Observe(t.Context(), parent.ID, "admission")
				testutil.FailErr(t, "observe execution", err)
				if observation.Settled != want {
					t.Fatalf("settlement=%+v, want %v", observation, want)
				}
			}
			check(false)
			admission, _, err := st.ClaimPromptSubmission(t.Context(), "admission")
			testutil.FailErr(t, "claim prompt", err)
			testutil.FailErr(t, "complete prompt", st.FinishPromptSubmission(t.Context(), admission.ID, admission.ClaimToken, store.PromptSubmissionComplete, "{}", store.PromptSubmissionFailure{}))
			check(true)
			foreign, err := st.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
			testutil.FailErr(t, "create unrelated session", err)
			testutil.FailErr(t, "mark unrelated session busy", st.SetSessionStatus(t.Context(), foreign.ID, api.SessionStatusBusy))
			check(true)
			testutil.FailErr(t, "mark child busy", st.SetSessionStatus(t.Context(), child.ID, api.SessionStatusBusy))
			check(false)
			testutil.FailErr(t, "mark child idle", st.SetSessionStatus(t.Context(), child.ID, api.SessionStatusIdle))
			check(true)
			checkpoints.pending = []api.CheckpointEvent{{SessionID: child.ID, ID: "approval"}}
			check(false)
			checkpoints.pending = nil
			check(true)
			release := manager.Runner.Coordinator.CoordinatorLoop().Admission.BeginPromptExecution(t.Context(), child.ID)
			check(false)
			release()
			check(true)
			for _, id := range []string{"child-failed", "child-recovered"} {
				_, _, err := st.PutPromptSubmission(t.Context(), store.PromptSubmission{ID: id, SessionID: child.ID, ProjectID: child.ProjectID, Origin: store.PromptSubmissionOriginUser, SubmittedBy: child.OwnerPersonID, InputDigest: id, InputJSON: "{}"})
				testutil.FailErr(t, "admit child execution", err)
				check(false)
				receipt, _, err := st.ClaimPromptSubmission(t.Context(), id)
				testutil.FailErr(t, "claim child execution", err)
				status := store.PromptSubmissionComplete
				failure := store.PromptSubmissionFailure{}
				if id == "child-failed" {
					status = store.PromptSubmissionFailed
					failure.Code = "provider_overloaded"
				}
				testutil.FailErr(t, "settle child execution", st.FinishPromptSubmission(t.Context(), id, receipt.ClaimToken, status, "{}", failure))
				check(true)
				observation, err := manager.Observations.Observe(t.Context(), parent.ID, "admission")
				testutil.FailErr(t, "observe latest child receipt", err)
				if (len(observation.Failures) == 1) != (id == "child-failed") {
					t.Fatalf("stale failure projection: %+v", observation.Failures)
				}
			}
			q := worker.NewInMemoryQueue(1)
			manager.SetWorkerQueue(q)
			jobID, err := q.Enqueue(t.Context(), api.WorkerTask{ParentSessionID: parent.ID, ProjectID: parent.ProjectID, Prompt: "work", Brief: "work", ExecutionTarget: api.ExecutionTargetLocal})
			testutil.FailErr(t, "enqueue worker", err)
			check(false)
			job, err := q.ClaimNext(t.Context(), worker.ClaimRequest{ExecutionTarget: api.ExecutionTargetLocal})
			testutil.FailErr(t, "claim worker", err)
			_, err = q.Complete(t.Context(), job, api.WorkerResult{Status: "needs_decision"})
			testutil.FailErr(t, "return decision", err)
			check(false)
			testutil.FailErr(t, "deliver decision", q.MarkOutcomeDelivered(t.Context(), jobID))
			check(true)
			if _, err := manager.Observations.Observe(t.Context(), child.ID, "admission"); err == nil {
				t.Fatal("foreign admission accepted")
			}
		})
	}
}

func TestExecutionObservationRejectsMissingSession(t *testing.T) {
	st := store.NewMemory()
	_, _, err := st.PutPromptSubmission(t.Context(), store.PromptSubmission{ID: "orphan", SessionID: "missing", Origin: store.PromptSubmissionOriginUser, SubmittedBy: testutil.HostOwner().ID})
	testutil.FailErr(t, "retain orphan admission", err)
	manager := session.NewHost(st, session.Models{Client: nil, Provider: nil, Limits: settings.SessionLimits{}, Cost: nil}, nil)
	if _, err := manager.Observations.Observe(t.Context(), "missing", "orphan"); err == nil {
		t.Fatal("missing session became a wait state")
	}
}

func TestExecutionTreeSupportsWorkflowStartsWithoutPromptAdmission(t *testing.T) {
	st := store.NewMemory()
	root, err := st.Create(t.Context(), api.CreateSessionRequest{}, "project")
	testutil.FailErr(t, "create workflow session", err)
	manager := session.NewHost(st, session.Models{Client: nil, Provider: nil, Limits: settings.SessionLimits{}, Cost: nil}, nil)
	observation, err := manager.Observations.Tree(t.Context(), root.ID)
	testutil.FailErr(t, "observe idle tree", err)
	if !observation.Settled || observation.SubmissionID != "" {
		t.Fatalf("workflow tree observation: %+v", observation)
	}
	release := manager.Runner.Coordinator.CoordinatorLoop().Admission.BeginPromptExecution(t.Context(), root.ID)
	defer release()
	observation, err = manager.Observations.Tree(t.Context(), root.ID)
	testutil.FailErr(t, "observe active workflow", err)
	if observation.Settled {
		t.Fatal("active workflow prompt was considered settled")
	}
	if _, err := manager.Observations.Tree(t.Context(), "missing"); err == nil {
		t.Fatal("missing workflow session accepted")
	}
}

const incidentPlanReviewPolicyPrompt = `Please give feedback on this design review.

Remember everything is greenfield, we do not allow migration. Might be worth reviewing agents.md to see our standards.`

type failingKickPromptEngine struct{ err error }

func (e failingKickPromptEngine) Render(context.Context, string, map[string]any) (string, error) {
	return "", e.err
}

func (e failingKickPromptEngine) RenderInject(context.Context, string, map[string]any) (string, error) {
	return "", e.err
}

func (e failingKickPromptEngine) RenderGuidance(context.Context, string, map[string]any) (string, error) {
	return "", e.err
}

func (e failingKickPromptEngine) RenderKick(context.Context, string, map[string]any) (string, error) {
	return "", e.err
}

func (failingKickPromptEngine) Register(string, string) error { return nil }

func TestPromptDoesNotQueueGreenfieldBuildKickForPlanReviewPolicyPrompt(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")
	store := store.NewMemory()
	rec := llm.NewRecordingClient(llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{Pattern: ".", Text: "ok"}}}))
	mgr := session.NewHost(store, session.Models{Client: rec, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	oartest.InstallCloseoutPolicy(t, mgr)
	wirePromptTestManager(t, mgr)
	mgr.SetPromptEngine(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{}))
	mgr.SetProgressStore(progress.NewMemoryStore())
	testutil.FailErr(t, "install anchor registry", mgr.Coordinator.Guidance.InstallAnchorRegistry())

	ctx := context.Background()
	sess, err := mgr.Chats.CreateForProject(ctx, testdbseed.DefaultProjectID, api.SessionPostureBuild)
	testutil.FailErr(t, "create coordinator session", err)
	if _, err := mgr.Submissions.Prompt(ctx, sess.ID, incidentPlanReviewPolicyPrompt); err != nil {
		testutil.FailErr(t, "mgr.Submissions.Prompt failed", err)
	}
	if id, ok := mgr.Runner.Coordinator.Kicks().PeekPendingKickID(sess.ID); ok && id != "" {
		t.Fatalf("TakePendingKickID = %q want empty (greenfield-build kick must not queue)", id)
	}
	for _, msg := range rec.LastRequest().Messages {
		if msg.Role == api.MessageRoleUser && strings.Contains(msg.Content, "Greenfield build — ship via plan") {
			t.Fatalf("greenfield-build kick must not prepend on policy prompt: %q", msg.Content)
		}
	}
}

func TestQueueCoordinatorKickPrependsOnPrompt(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")
	store := store.NewMemory()
	rec := llm.NewRecordingClient(llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{Pattern: ".", Text: "ok"}}}))
	mgr := session.NewHost(store, session.Models{Client: rec, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	oartest.InstallCloseoutPolicy(t, mgr)
	wirePromptTestManager(t, mgr)
	mgr.SetPromptEngine(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{}))

	ctx := context.Background()

	sess, err := store.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)
	mgr.Coordinator.Guidance.Emit(context.Background(), sess.ID, anchor.ComposeDone, anchor.Envelope{})
	if _, err := mgr.Submissions.Prompt(ctx, sess.ID, "continue"); err != nil {
		testutil.FailErr(t, "mgr.Submissions.Prompt failed", err)
	}
	req := rec.LastRequest()
	foundKick := false
	for _, msg := range req.Messages {
		if msg.Role == api.MessageRoleSystem && msg.Origin == api.MessageOriginHost && strings.Contains(msg.Content, "Compose succeeded") {
			foundKick = true
			break
		}
	}
	if !foundKick {
		t.Fatal("expected compose-done kick nudge as host system message")
	}
	msgs, err := store.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "GetMessages", err)
	if len(msgs) < 2 {
		t.Fatalf("messages = %+v want kick + user rows", msgs)
	}
	var kickRow, userRow *api.Message
	for i := range msgs {
		if strings.Contains(msgs[i].Content, "Compose succeeded") {
			kickRow = &msgs[i]
		}
		if msgs[i].Content == "continue" {
			userRow = &msgs[i]
		}
	}
	if kickRow == nil || kickRow.Visibility != api.MessageVisibilityInternal {
		t.Fatalf("kick visibility = %+v want internal", kickRow)
	}
	if userRow == nil || userRow.Visibility == api.MessageVisibilityInternal {
		t.Fatalf("user row = %+v want transcript-visible", userRow)
	}
}

func TestPromptRenderFailurePreservesCoordinatorKickForRetry(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")
	st := store.NewMemory()
	rec := llm.NewRecordingClient(llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{Pattern: ".", Text: "ok"}}}))
	mgr := session.NewHost(st, session.Models{Client: rec, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	oartest.InstallCloseoutPolicy(t, mgr)
	wirePromptTestManager(t, mgr)
	testutil.FailErr(t, "install anchor registry", mgr.Coordinator.Guidance.InstallAnchorRegistry())
	mgr.SetPromptEngine(failingKickPromptEngine{err: errors.New("template unavailable")})

	ctx := context.Background()
	sess, err := st.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	mgr.Coordinator.Guidance.Emit(ctx, sess.ID, anchor.ComposeDone, anchor.Envelope{})
	if _, err := mgr.Submissions.Prompt(ctx, sess.ID, "continue"); err == nil {
		t.Fatal("Prompt error = nil, want kick render failure")
	}
	if id, ok := mgr.Runner.Coordinator.Kicks().PeekPendingKickID(sess.ID); !ok || !anchor.SameInform(id, anchor.ComposeDone) {
		t.Fatalf("pending kick after render failure = (%q, %v), want compose-done", id, ok)
	}

	mgr.SetPromptEngine(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{}))
	if _, err := mgr.Submissions.Prompt(ctx, sess.ID, "continue"); err != nil {
		testutil.FailErr(t, "retry prompt", err)
	}
	if id, ok := mgr.Runner.Coordinator.Kicks().PeekPendingKickID(sess.ID); ok || id != "" {
		t.Fatalf("pending kick after successful retry = (%q, %v), want empty", id, ok)
	}
}
