package session_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/kick"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/session"
	sessiondecisions "github.com/lycaon/lycaon/internal/session/decisions"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testbaseline"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
)

func overlaySourceFixture(t *testing.T) (primary, overlay string, scope api.TaskScope, baseline string) {
	t.Helper()
	primary = t.TempDir()
	overlay = filepath.Join(primary, settingsoverlay.DirName(), "overlays", "job-source")
	testutil.FailErr(t, "MkdirAll", os.MkdirAll(overlay, 0o755))
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(overlay, "game.py"), []byte("print(1)\n"), 0o644))
	scope = api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"game.py"}}
	snap := testbaseline.Capture(t, primary)
	raw := snap
	return primary, overlay, scope, raw
}

func currentVerifyPassedMessage(root string) api.Message {
	revision, digest := invocation.SourceRevisionForRoot(root)
	return api.Message{
		Role: api.MessageRoleTool,
		ToolResult: &api.ToolResult{
			Content:  `{"outcome":"passed"}`,
			ToolArgs: map[string]any{"command": "selected-check"},
			Invocation: &api.InvocationReceipt{
				ID: "receipt-verify", Tool: "verify", Status: api.InvocationStatusCompleted,
				Evidence:       api.InvocationEvidence{Kind: "result", Ref: "message-1"},
				SourceRevision: revision, SourceRootDigest: digest,
				SourceVerdict: api.SourceVerdictPassed,
			},
		},
	}
}

func TestBuildImplementSessionStateListsLedgerPendingOverlays(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemory()
	mgr := session.NewHost(mem, session.Models{Client: llm.NewMockProvider(&llm.MockConfig{}), Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	q := worker.NewInMemoryQueue(4)
	mgr.SetWorkerQueue(q)
	parent, err := mem.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create parent", err)

	primary, overlay, scope, baseline := overlaySourceFixture(t)
	child, err := mem.CreateChild(ctx, parent, api.SpawnChildRequest{AgentType: "implementer"})
	testutil.FailErr(t, "CreateChild", err)
	testutil.FailErr(t, "AppendMessages", mem.AppendMessages(ctx, child.ID, currentVerifyPassedMessage(overlay)))

	uncheckedPrimary, uncheckedOverlay, uncheckedScope, uncheckedBaseline := overlaySourceFixture(t)
	uncheckedChild, err := mem.CreateChild(ctx, parent, api.SpawnChildRequest{AgentType: "implementer"})
	testutil.FailErr(t, "CreateChild unchecked", err)

	_, err = q.EnqueueWithProjectID(ctx, testdbseed.DefaultProjectID, api.WorkerTask{
		Prompt:          "fixture",
		Brief:           "fixture",
		ParentSessionID: parent.ID, ProjectID: testdbseed.DefaultProjectID,
		WorkspacePath: primary, WorkspaceRoot: overlay, ChildSessionID: child.ID,
		AgentType: "implementer", Status: api.WorkerStatusComplete,
		MergeStatus: api.WorkerMergeStatusPending, Scope: &scope, WorkspaceBaselinePath: baseline,
	})
	testutil.FailErr(t, "enqueue checked", err)
	_, err = q.EnqueueWithProjectID(ctx, testdbseed.DefaultProjectID, api.WorkerTask{
		Prompt:          "fixture",
		Brief:           "fixture",
		ParentSessionID: parent.ID, ProjectID: testdbseed.DefaultProjectID,
		WorkspacePath: uncheckedPrimary, WorkspaceRoot: uncheckedOverlay, ChildSessionID: uncheckedChild.ID,
		AgentType: "implementer", Status: api.WorkerStatusComplete,
		MergeStatus: api.WorkerMergeStatusPending, Scope: &uncheckedScope, WorkspaceBaselinePath: uncheckedBaseline,
	})
	testutil.FailErr(t, "enqueue unchecked", err)

	state := mgr.Workers.State.ForSession(ctx, parent)
	if len(state.PendingOverlayIDs) != 2 {
		t.Fatalf("PendingOverlayIDs = %v want both ledger-pending overlays", state.PendingOverlayIDs)
	}
}

func TestCompleteWriteWorkerThenHostCycleSelectsOverlayPromote(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemory()
	mgr := session.NewHost(mem, session.Models{Client: llm.NewMockProvider(&llm.MockConfig{}), Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	q := worker.NewInMemoryQueue(4)
	mgr.SetWorkerQueue(q)
	parent, err := mem.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create parent", err)

	primary, overlay, scope, baseline := overlaySourceFixture(t)
	jobID, err := q.EnqueueWithProjectID(ctx, testdbseed.DefaultProjectID, api.WorkerTask{
		Prompt:          "fixture",
		Brief:           "fixture",
		ParentSessionID: parent.ID, ProjectID: testdbseed.DefaultProjectID,
		WorkspacePath: primary, WorkspaceRoot: overlay,
		AgentType: "implementer", Status: api.WorkerStatusPending,
		Scope: &scope, WorkspaceBaselinePath: baseline,
	})
	testutil.FailErr(t, "enqueue worker", err)
	claimed, err := q.ClaimNext(ctx, worker.ClaimRequest{ClaimedBy: "test", ExecutionTarget: api.ExecutionTargetLocal})
	testutil.FailErr(t, "claim worker", err)
	won, err := q.Complete(ctx, claimed, api.WorkerResult{Status: "partial", Summary: "unmet verify"})
	testutil.FailErr(t, "complete", err)
	if !won {
		t.Fatal("completion claim lost")
	}

	state := mgr.Workers.State.ForSession(ctx, parent)
	if len(state.PendingOverlayIDs) != 1 || state.PendingOverlayIDs[0] != jobID {
		t.Fatalf("PendingOverlayIDs = %v want [%s] after Complete", state.PendingOverlayIDs, jobID)
	}

	history := []api.Message{
		{
			Role:          api.MessageRoleAssistant,
			Content:       `<task job_id="` + jobID + `" agent_type="implementer" state="partial" merge_status="pending"><summary>unmet verify</summary></task>`,
			WorkerSummary: &api.WorkerSummaryMeta{WorkerID: jobID, Status: "partial"},
		},
		{
			Role:       api.MessageRoleUser,
			Origin:     api.MessageOriginHost,
			Visibility: api.MessageVisibilityInternal,
			Kind:       api.MessageKindHostLoopWake,
			Content:    surface.HostLoopWakeSentinel,
		},
	}
	profile := surface.ResolveTurnProfile(
		api.CoordinatorRunContext{},
		parent,
		history,
		state,
	)
	if profile.SurfaceID != surface.SurfaceImplementOverlayPromote {
		t.Fatalf("surface = %q want implement_overlay_promote after complete+pending", profile.SurfaceID)
	}
}

type failingSecretInheritanceStore struct{ *store.Memory }

func (*failingSecretInheritanceStore) SeedSecretExposure(context.Context, string) error {
	return errors.New("evidence store unavailable")
}

func TestReadSensitivePathSetsSecretExposure(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemory()
	sess, err := mem.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create", err)

	dir := t.TempDir()
	_, _, err = mem.CommitEvidenceToolResult(ctx, sess.ID, dir, "read", map[string]any{"path": "main.go"}, `{"path":"main.go","content":"package main"}`)
	testutil.FailErr(t, "read main.go", err)
	exposed, err := mem.SessionSecretExposure(ctx, sess.ID)
	testutil.FailErr(t, "read secret exposure after source read", err)
	if exposed {
		t.Fatal("reading main.go must not set secret exposure")
	}

	_, _, err = mem.CommitEvidenceToolResult(ctx, sess.ID, dir, "read", map[string]any{"path": ".env"}, `{"path":".env","content":"KEY=x"}`)
	testutil.FailErr(t, "read .env", err)
	exposed, err = mem.SessionSecretExposure(ctx, sess.ID)
	testutil.FailErr(t, "read secret exposure after credential read", err)
	if !exposed {
		t.Fatal("reading .env must set secret exposure")
	}
}

func TestSecretExposureInheritAndMerge(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemory()
	mgr := session.NewHost(mem, session.Models{Client: llm.NewMockProvider(&llm.MockConfig{}), Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	parent, err := mem.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create parent", err)
	testutil.FailErr(t, "seed parent", mem.SeedSecretExposure(ctx, parent.ID))
	exposed, err := mem.SessionSecretExposure(ctx, parent.ID)
	testutil.FailErr(t, "read parent exposure", err)
	if !exposed {
		t.Fatal("parent should be secret-exposed after seed")
	}

	child, err := mgr.Workers.SpawnChild(ctx, parent.ID, api.SpawnChildRequest{AgentType: "implementer", Prompt: "do work"})
	testutil.FailErr(t, "SpawnChild", err)
	exposed, err = mem.SessionSecretExposure(ctx, child.ID)
	testutil.FailErr(t, "read child exposure", err)
	if !exposed {
		t.Fatal("spawned child must inherit secret exposure")
	}

	parent2, err := mem.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create parent2", err)
	child2, err := mem.CreateChild(ctx, parent2, api.SpawnChildRequest{AgentType: "implementer", Prompt: "x"})
	testutil.FailErr(t, "create child2", err)
	testutil.FailErr(t, "seed child2", mem.SeedSecretExposure(ctx, child2.ID))
	testutil.FailErr(t, "merge", workeroutcomes.MergeWorkerSecretExposureIntoParent(ctx, mem, parent2.ID, child2.ID))
	exposed, err = mem.SessionSecretExposure(ctx, parent2.ID)
	testutil.FailErr(t, "read merged parent exposure", err)
	if !exposed {
		t.Fatal("parent must pick up child secret exposure on merge")
	}
}

func TestSpawnChildRollsBackWhenSecurityInheritanceFails(t *testing.T) {
	ctx := t.Context()
	mem := store.NewMemory()
	parent, err := mem.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create parent", err)
	testutil.FailErr(t, "seed parent", mem.SeedSecretExposure(ctx, parent.ID))
	failing := &failingSecretInheritanceStore{Memory: mem}
	mgr := session.NewHost(failing, session.Models{Client: llm.NewMockProvider(&llm.MockConfig{}), Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())

	if _, err := mgr.Workers.SpawnChild(ctx, parent.ID, api.SpawnChildRequest{AgentType: "implementer"}); err == nil {
		t.Fatal("SpawnChild succeeded without inheriting secret exposure")
	}
	sessions, err := mem.List(ctx)
	testutil.FailErr(t, "list sessions", err)
	if len(sessions) != 1 || sessions[0].ID != parent.ID {
		t.Fatalf("sessions after failed spawn = %+v", sessions)
	}
}

func TestSessionSecretExposureSQLRebuild(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "secret.db")
	hot := store.NewSQL(sqlDB)
	ctx := context.Background()
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, t.TempDir())
	sess, err := hot.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create", err)

	testutil.FailErr(t, "seed", hot.SeedSecretExposure(ctx, sess.ID))
	cold := store.NewSQL(sqlDB)
	exposed, err := cold.SessionSecretExposure(ctx, sess.ID)
	testutil.FailErr(t, "rebuild secret exposure", err)
	if !exposed {
		t.Fatal("SQL rebuild-from-records must report secret exposure")
	}
}

func TestAppendWorkerSummaryCarriesStructuredDecisionRequest(t *testing.T) {
	ctx := context.Background()
	store := store.NewMemory()
	mgr := session.NewHost(store, session.Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	decisions := sessiondecisions.NewMemory()
	mgr.SetDecisionStore(decisions)

	parent, err := store.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create parent", err)
	child, err := store.CreateChild(ctx, parent, api.SpawnChildRequest{AgentType: "implementer"})
	testutil.FailErr(t, "create child", err)

	testutil.FailErr(t, "seed task row", store.AppendMessages(ctx, parent.ID,
		api.Message{ID: "a1", Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{ID: "tc1", Name: "task"}}},
		api.Message{
			ID: "t1", Role: api.MessageRoleTool, Content: `{"job_id":"job-dec","status":"enqueued"}`,
			ToolResult: &api.ToolResult{
				Tool: "task", ToolCallID: "tc1", AssistantMessageID: "a1",
				Dispatch: &api.WorkerDispatch{WorkerID: "job-dec"},
				Content:  `{"job_id":"job-dec","status":"enqueued"}`,
			},
		},
	))

	testutil.FailErr(t, "store decision", decisions.Put(ctx, api.WorkerDecisionRequest{
		ChildSessionID: child.ID, WorkerID: "job-dec", Question: "Fix host cache lifecycle or use project scratch?",
		Options:      []string{"ask user to chown cache", "retry under .paintedwolf/scratch/"},
		BlockerClass: api.WorkerBlockerSandbox,
	}))

	status, err := mgr.Workers.Summaries.Append(ctx, parent.ID, workeroutcomes.SummaryInput{
		JobID:          "job-dec",
		ChildSessionID: child.ID,
		AgentType:      "implementer",
		Status:         "complete",
	})
	testutil.FailErr(t, "append summary", err)
	if status != string(api.WorkerSummaryStatusNeedsDecision) {
		t.Fatalf("status = %q", status)
	}

	msgs, err := store.GetMessages(ctx, parent.ID)
	testutil.FailErr(t, "parent messages", err)
	var content string
	for _, m := range msgs {
		if m.ID == "t1" {
			if m.WorkerSummary != nil {
				content = m.WorkerSummary.Envelope
			}
			if content == "" {
				content = m.Content
			}
			break
		}
	}
	env, ok := workercompletion.ParseWorkerCompletionEnvelope(content)
	if !ok || env.DecisionRequest == nil {
		t.Fatalf("parse decision: ok=%v content=%q", ok, content)
	}
	if env.DecisionRequest.Question != "Fix host cache lifecycle or use project scratch?" {
		t.Fatalf("question = %q", env.DecisionRequest.Question)
	}
	if env.DecisionRequest.BlockerClass != api.WorkerBlockerSandbox {
		t.Fatalf("blocker = %q", env.DecisionRequest.BlockerClass)
	}
	if env.DecisionRequest.ChildSessionID != child.ID || len(env.DecisionRequest.Options) != 2 {
		t.Fatalf("decision = %+v", env.DecisionRequest)
	}
	if env.Digest != "" || strings.Contains(content, "<digest>") {
		t.Fatalf("unexpected envelope digest=%q content=%q", env.Digest, content)
	}

	anchorEnv := mgr.Workers.Results.EnvelopeForTerminal(ctx, parent.ID, "job-dec")
	req, ok := kick.WorkerDecisionRequestFromOptions(anchorEnv.KickOptions())
	if !ok {
		t.Fatal("envelope missing last_worker_decision_request")
	}
	if req.Question != "Fix host cache lifecycle or use project scratch?" {
		t.Fatalf("kick question = %q", req.Question)
	}
	if req.BlockerClass != api.WorkerBlockerSandbox || req.ChildSessionID != child.ID {
		t.Fatalf("kick decision = %+v", req)
	}
}

func TestWorkerProjectionWaitsForOriginalDispatchResponse(t *testing.T) {
	for _, tool := range []string{"task", "delegate_dispatch"} {
		t.Run(tool, func(t *testing.T) {
			ctx := context.Background()
			messages := store.NewMemory()
			mgr := session.NewHost(messages, session.Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
			queue := worker.NewInMemoryQueue(2)
			mgr.SetWorkerQueue(queue)
			parent, err := messages.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
			testutil.FailErr(t, "create parent", err)
			jobID, err := queue.Enqueue(ctx, api.WorkerTask{
				ParentSessionID: parent.ID, SourceToolCallID: "call-dispatch", SourceArgsDigest: "digest",
				ProjectID: testdbseed.DefaultProjectID, WorkspacePath: t.TempDir(), AgentType: "implementer",
				Prompt: "Update the report", Brief: "Update the report", Status: api.WorkerStatusPending,
			})
			testutil.FailErr(t, "enqueue worker", err)
			testutil.FailErr(t, "append dispatch call", messages.AppendMessages(ctx, parent.ID, api.Message{
				ID: "assistant", Role: api.MessageRoleAssistant, Origin: api.MessageOriginModel,
				ToolCalls: []api.ToolCall{{ID: "call-dispatch", Name: tool}},
			}))
			summary := workerSummaryFixture(jobID, "child", "implementer", api.WorkerSummaryStatusFailed)
			if err := mgr.Workers.Cards.Project(ctx, parent.ID, jobID, summary); err == nil {
				t.Fatal("terminal projection must remain pending until its dispatch response exists")
			}
			pending, err := messages.GetMessages(ctx, parent.ID)
			testutil.FailErr(t, "read pending conversation", err)
			if len(pending) != 1 {
				t.Fatalf("pending conversation has %d messages; synthetic calls must not split the open tool group", len(pending))
			}
			result := &api.ToolResult{ToolCallID: "call-dispatch", AssistantMessageID: "assistant", Content: "queued"}
			guidance.ApplyTaskDispatchMetadata(tool, result, &api.WorkerDispatch{WorkerID: jobID})
			testutil.FailErr(t, "append original response", messages.AppendMessages(ctx, parent.ID, api.Message{
				ID: "response", Role: api.MessageRoleTool, Origin: api.MessageOriginTool, Content: "queued", ToolResult: result,
			}))
			testutil.FailErr(t, "retry terminal projection", mgr.Workers.Cards.Project(ctx, parent.ID, jobID, summary))
			projected, err := messages.GetMessages(ctx, parent.ID)
			testutil.FailErr(t, "read projected conversation", err)
			if len(projected) != 2 || projected[1].ID != "response" || projected[1].WorkerSummary == nil || projected[1].ToolResult.ToolCallID != "call-dispatch" {
				t.Fatalf("completion did not update the original dispatch response: %+v", projected)
			}
		})
	}
}
