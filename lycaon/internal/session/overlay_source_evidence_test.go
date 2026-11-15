package session_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/kick"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/inspector"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/observation"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/session"
	sessiondecisions "github.com/lycaon/lycaon/internal/session/decisions"
	"github.com/lycaon/lycaon/internal/session/promptinput"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/verification"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/session/workercontext"
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
	"github.com/lycaon/lycaon/internal/session/workerworkspace"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testbaseline"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/survey"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/internal/workspace"
	"github.com/lycaon/lycaon/pkg/api"
)

func overlaySourceFixture(t *testing.T) (primary, overlay string, scope api.TaskScope, baseline string) {
	t.Helper()
	primary = t.TempDir()
	overlay = filepath.Join(primary, settingsoverlay.DirName(), "overlays", "job-source")
	testutil.FailErr(t, "MkdirAll overlay", os.MkdirAll(filepath.Join(overlay, "pkg"), 0o755))
	testutil.FailErr(t, "MkdirAll primary pkg", os.MkdirAll(filepath.Join(primary, "pkg"), 0o755))
	testutil.FailErr(t, "WriteFile primary game", os.WriteFile(filepath.Join(primary, "game.py"), []byte("print(0)\n"), 0o644))
	testutil.FailErr(t, "WriteFile primary dep", os.WriteFile(filepath.Join(primary, "pkg", "dep.py"), []byte("dep = 1\n"), 0o644))
	testutil.FailErr(t, "WriteFile primary dirty", os.WriteFile(filepath.Join(primary, "dirty_upper.py"), []byte("x = 1\n"), 0o644))
	testutil.FailErr(t, "WriteFile overlay game", os.WriteFile(filepath.Join(overlay, "game.py"), []byte("print(100)\n"), 0o644))
	testutil.FailErr(t, "WriteFile overlay dep", os.WriteFile(filepath.Join(overlay, "pkg", "dep.py"), []byte("dep = 1\n"), 0o644))
	testutil.FailErr(t, "WriteFile overlay dirty", os.WriteFile(filepath.Join(overlay, "dirty_upper.py"), []byte("x = 1\n"), 0o644))
	meta := enginepaths.MetaDirForBranchRoot(overlay)
	testutil.FailErr(t, "MkdirAll meta", os.MkdirAll(meta, 0o700))
	testutil.FailErr(t, "WriteJobMeta", workspace.WriteJobMeta(meta, workspace.JobMeta{
		SnapshotComplete: true,
		Roots:            []workspace.JobMetaRoot{{ID: "primary", Label: "primary", Path: primary, IsPrimary: true}},
	}))
	scope = api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"game.py", "dirty_upper.py", "pkg/dep.py"}}
	snap := testbaseline.Capture(t, primary)
	return primary, overlay, scope, snap
}

func currentVerifyPassedMessage(root string) api.Message {
	revision, digest := invocation.SourceRevisionForRoot(root)
	return api.Message{
		Role: api.MessageRoleTool,
		ToolResult: &api.ToolResult{
			Content:  `{"outcome":"passed"}`,
			ToolArgs: map[string]any{"command": "selected-check"},
			Invocation: &api.InvocationReceipt{
				ID: "receipt-verify", Tool: "verify", ToolCallID: "check-verify-1", Status: api.InvocationStatusCompleted,
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
	t.Cleanup(mgr.BeginEngineShutdown)
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
	t.Cleanup(mgr.BeginEngineShutdown)
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

func TestHostEndToEndWorkerWorkspaceObservationVerificationAndNextTurnNotice(t *testing.T) {
	ctx := context.Background()
	mem, reg, mgr, q := newObservationHost(t)

	parent, err := mem.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create parent", err)

	primary, overlay, scope, baseline := overlaySourceFixture(t)
	rev, rootDigest := invocation.SourceRevisionForRoot(overlay)
	curRev := workercompletion.SourceRevision{Revision: rev, RootDigest: rootDigest}
	mgr.Verification.SetRevisionSource(func(context.Context, string) (string, string) {
		return rev, rootDigest
	})

	child, err := mem.CreateChild(ctx, parent, api.SpawnChildRequest{AgentType: "implementer"})
	testutil.FailErr(t, "CreateChild", err)
	child.WorkspacePath = primary
	child.ProjectID = testdbseed.DefaultProjectID

	jobID, err := q.EnqueueWithProjectID(ctx, testdbseed.DefaultProjectID, api.WorkerTask{
		Prompt: "fixture", Brief: "fixture", ParentSessionID: parent.ID, ProjectID: testdbseed.DefaultProjectID,
		WorkspacePath: primary, WorkspaceRoot: overlay, ChildSessionID: child.ID,
		AgentType: "implementer", Status: api.WorkerStatusRunning, Scope: &scope, WorkspaceBaselinePath: baseline,
	})
	testutil.FailErr(t, "EnqueueWithProjectID", err)
	task, ok := q.Get(jobID)
	if !ok || task == nil {
		t.Fatalf("expected task %s in queue", jobID)
	}

	// A second worker on an unrelated root verifies repochange root isolation.
	other := provisionUnrelatedWorker(t, ctx, mem, mgr, q, reg, parent, rev, rootDigest)

	workerCtx := workercontext.WithJob(ctx, jobID)
	tctx, err := mgr.Workers.Workspaces.Enrich(workerCtx, child, tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: child.ID, WorkerJobID: jobID},
	})
	testutil.FailErr(t, "Workspaces.Enrich", err)

	tracker := mgr.Workers.Workspaces.TrackerFor(jobID)
	if tracker == nil {
		t.Fatal("expected Enrich to provision a scoped observation.Tracker on mgr.Workers.Workspaces")
	}
	unboundFacts := tracker.Facts()
	if unboundFacts.Mode != observation.VisibilityModeUnknown || unboundFacts.LiveVisible || unboundFacts.SupportsInvalidation || unboundFacts.Completeness != observation.CompletenessUnknown {
		t.Fatalf("expected unbound tracker to default to conservative unknown visibility/completeness, got %+v", unboundFacts)
	}
	_, err = mgr.Workers.Workspaces.BindViewCapabilities(
		observation.MountedAdapterViewCapabilities(jobID, "fuselinux", true, true, true, observation.CompletenessComplete),
		jobID,
	)
	testutil.FailErr(t, "BindViewCapabilities job", err)

	// Execute ordinary ReadTool calls (without workerharness.Service.Read and without calling ObserveReadPath).
	_, err = reg.Run(workerCtx, "read", map[string]any{"path": "game.py"}, tctx)
	testutil.FailErr(t, "ordinary ReadTool game.py", err)
	_, err = reg.Run(workerCtx, "read", map[string]any{"path": "pkg/dep.py"}, tctx)
	testutil.FailErr(t, "ordinary ReadTool pkg/dep.py", err)

	depPath := layeredfs.QualifiedPath{RootID: "primary", Rel: "pkg/dep.py"}
	depObs, ok := tracker.LatestObservationForPath(depPath)
	if !ok || depObs.SHA256 != textfile.SHA256([]byte("dep = 1\n")) || depObs.Completeness != observation.CompletenessComplete || depObs.Layer != layeredfs.LayerTrunk {
		t.Fatalf("expected ordinary ReadTool to record complete structured observation for pkg/dep.py, got %+v (ok=%v)", depObs, ok)
	}

	// Perform a worker write on dirty_upper.py so it is pinned in LayerPrivate.
	testutil.FailErr(t, "BeforeWorkerWrite", mgr.Workers.Workspaces.BeforeWorkerWrite(workerCtx, tctx, "dirty_upper.py"))
	mgr.Workers.Workspaces.AfterWorkerWrite(workerCtx, tctx, "dirty_upper.py")

	// Record passing verification evidence on the host's Verification service.
	verifyMsg := currentVerifyPassedMessage(overlay)
	testutil.FailErr(t, "AppendMessages", mem.AppendMessages(ctx, child.ID, verifyMsg))
	mgr.Verification.RecordSourceRunTerminal(workerCtx, child, parent.ID, verification.ProducerVerify, tools.SourceRunCapture{
		CheckID:          "check-verify-1",
		IsCheck:          true,
		Command:          "selected-check",
		Cwd:              ".",
		ExitCode:         0,
		Verdict:          api.SourceVerdictPassed,
		SourceRevision:   rev,
		SourceRootDigest: rootDigest,
	}, time.Now().UTC())

	childHistory, err := mem.GetMessages(ctx, child.ID)
	testutil.FailErr(t, "GetMessages", err)

	// 1. Unobserved lower source change via real repochange.Notify keeps GateState and OverlaySourceProof valid.
	testutil.FailErr(t, "WriteFile unrelated", os.WriteFile(filepath.Join(primary, "unrelated.py"), []byte("u = 2\n"), 0o644))
	repochange.Notify(ctx, repochange.Event{
		ProjectDir: primary,
		Kind:       repochange.WorktreeChanged,
		Paths:      []string{"unrelated.py"},
		Source:     repochange.SourceWatcher,
	})
	if passed, _, _ := mgr.Verification.GateState(workerCtx, child, childHistory); !passed {
		t.Fatal("expected unobserved lower change to keep Verification.GateState passed")
	}
	proofFresh := session.OverlaySourceProofWithWorkspaces(workerCtx, mgr.Workers.Workspaces, task, childHistory, "selected-check", curRev)
	if status, _, _ := workercompletion.SourceEvidenceView(proofFresh); status != workercompletion.EvidenceStatusSatisfied {
		t.Fatalf("expected unobserved lower change to preserve satisfied OverlaySourceProof, got %s", status)
	}

	// 2. Lower source change on a dirty upper override (dirty_upper.py) via repochange.Notify stays pinned and immune.
	testutil.FailErr(t, "WriteFile dirty lower", os.WriteFile(filepath.Join(primary, "dirty_upper.py"), []byte("x = 999\n"), 0o644))
	repochange.Notify(ctx, repochange.Event{
		ProjectDir: primary,
		Kind:       repochange.WorktreeChanged,
		Paths:      []string{"dirty_upper.py"},
		Source:     repochange.SourceWatcher,
	})
	if passed, _, _ := mgr.Verification.GateState(workerCtx, child, childHistory); !passed {
		t.Fatal("expected dirty upper override to remain immune to lower trunk change")
	}
	if assembled, err := mgr.Runner.Preparation.Build(workerCtx, child, child.ID, promptinput.Input{}, ""); err != nil || assembled.TrunkNotice != nil {
		t.Fatalf("expected no trunk.changed notice for unobserved/dirty-upper paths, got err=%v notice=%+v", err, assembled.TrunkNotice)
	}

	// 3. Lower source change on observed pkg/dep.py via real repochange.Notify invalidates GateState &
	// OverlaySourceProof and queues trunk.changed.
	testutil.FailErr(t, "WriteFile dep v2", os.WriteFile(filepath.Join(primary, "pkg", "dep.py"), []byte("dep = 2\n"), 0o644))
	repochange.Notify(ctx, repochange.Event{
		ProjectDir: primary,
		Kind:       repochange.WorktreeChanged,
		Changes:    []repochange.WorktreeChange{{Path: "pkg/dep.py", Kind: repochange.WorktreeChangeStructural}},
		Source:     repochange.SourceWatcher,
	})
	assertObservedChangeInvalidatesProof(t, workerCtx, mgr, task, childHistory, curRev, depPath)
	if passed, _, _ := mgr.Verification.GateState(workerCtx, child, childHistory); passed {
		t.Fatal("expected Verification.GateState to be invalidated after observed lower path changed")
	}

	// Verify the second worker on otherPrimary was NOT invalidated or notified by events on primary.
	if passed, _, _ := mgr.Verification.GateState(other.ctx, other.child, other.history); !passed {
		t.Fatal("expected worker on unrelated root to remain passed after repochange on primary")
	}
	if otherAssess, ok := session.OverlayObservationAssessmentWithWorkspaces(mgr.Workers.Workspaces, other.task); !ok || otherAssess.VerificationInvalidated {
		t.Fatalf("expected worker on unrelated root to remain fresh, got %+v (ok=%v)", otherAssess, ok)
	}

	assertTrunkNoticeDeliveredOnce(t, workerCtx, mgr, child)

	// 4. Watcher overflow via real repochange.Notify (WorktreeChangeResync) marks matching root uncertain
	// without affecting unrelated root.
	repochange.Notify(ctx, repochange.Event{
		ProjectDir: other.primary,
		Kind:       repochange.WorktreeChanged,
		Changes:    []repochange.WorktreeChange{{Path: ".", Kind: repochange.WorktreeChangeResync}},
		Source:     repochange.SourceWatcher,
	})
	if otherAssess, ok := session.OverlayObservationAssessmentWithWorkspaces(mgr.Workers.Workspaces, other.task); !ok || !otherAssess.VerificationInvalidated || otherAssess.Freshness.StaleReason != observation.StaleReasonWatcherOverflowUncertain {
		t.Fatalf("expected watcher resync event on otherPrimary to mark otherTask stale with watcher_overflow_uncertain, got %+v (ok=%v)", otherAssess, ok)
	}

	// 5. ReleaseReservations cleans up the scoped workspace tracker.
	testutil.FailErr(t, "ReleaseReservations", mgr.Workers.Workspaces.ReleaseReservations(ctx, parent.ID, jobID))
	if mgr.Workers.Workspaces.TrackerFor(jobID) != nil {
		t.Fatal("expected ReleaseReservations to clean up scoped tracker for jobID")
	}
}

type unrelatedWorker struct {
	ctx     context.Context
	child   *api.Session
	task    *api.WorkerTask
	history []api.Message
	primary string
}

func newObservationHost(t *testing.T) (*store.Memory, *tools.StubRegistry, *session.Host, *worker.InMemoryQueue) {
	t.Helper()
	mem := store.NewMemory()
	reg := tools.NewStubRegistry()
	readTool := &survey.ReadTool{}
	testutil.FailErr(t, "Register read tool", reg.Register(readTool.Name(), readTool.Run))

	mgr := session.NewHost(
		mem,
		session.Models{
			Client:   llm.NewMockProvider(&llm.MockConfig{}),
			Provider: nil,
			Limits:   settings.DefaultSessionLimits(),
			Cost:     nil,
		},
		reg,
	)
	t.Cleanup(mgr.BeginEngineShutdown)
	dataDir := t.TempDir()
	mgr.Verification.SetDataDir(dataDir)
	mgr.Verification.SetEvidenceStore(inspector.NewJSONLStore(inspector.DefaultEvidenceDir))

	q := worker.NewInMemoryQueue(4)
	mgr.SetWorkerQueue(q)

	return mem, reg, mgr, q
}

func provisionUnrelatedWorker(
	t *testing.T,
	ctx context.Context,
	mem *store.Memory,
	mgr *session.Host,
	q *worker.InMemoryQueue,
	reg *tools.StubRegistry,
	parent *api.Session,
	rev, rootDigest string,
) unrelatedWorker {
	t.Helper()
	otherPrimary, otherOverlay, otherScope, otherBaseline := overlaySourceFixture(t)
	otherChild, err := mem.CreateChild(ctx, parent, api.SpawnChildRequest{AgentType: "implementer"})
	testutil.FailErr(t, "CreateChild other", err)
	otherChild.WorkspacePath = otherPrimary
	otherChild.ProjectID = testdbseed.DefaultProjectID
	otherJobID, err := q.EnqueueWithProjectID(ctx, testdbseed.DefaultProjectID, api.WorkerTask{
		Prompt: "other", Brief: "other", ParentSessionID: parent.ID, ProjectID: testdbseed.DefaultProjectID,
		WorkspacePath: otherPrimary, WorkspaceRoot: otherOverlay, ChildSessionID: otherChild.ID,
		AgentType: "implementer", Status: api.WorkerStatusRunning, Scope: &otherScope, WorkspaceBaselinePath: otherBaseline,
	})
	testutil.FailErr(t, "EnqueueWithProjectID other", err)
	otherTask, _ := q.Get(otherJobID)
	otherCtx := workercontext.WithJob(ctx, otherJobID)
	_, err = mgr.Workers.Workspaces.BindViewCapabilities(
		observation.MountedAdapterViewCapabilities(otherJobID, "fuselinux", true, true, true, observation.CompletenessComplete),
		otherJobID,
	)
	testutil.FailErr(t, "BindViewCapabilities other", err)
	otherTctx, err := mgr.Workers.Workspaces.Enrich(otherCtx, otherChild, tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: otherChild.ID, WorkerJobID: otherJobID},
	})
	testutil.FailErr(t, "Workspaces.Enrich other", err)
	_, err = reg.Run(otherCtx, "read", map[string]any{"path": "pkg/dep.py"}, otherTctx)
	testutil.FailErr(t, "ordinary ReadTool other", err)
	otherVerifyMsg := currentVerifyPassedMessage(otherOverlay)
	testutil.FailErr(t, "AppendMessages other", mem.AppendMessages(ctx, otherChild.ID, otherVerifyMsg))
	mgr.Verification.RecordSourceRunTerminal(otherCtx, otherChild, parent.ID, verification.ProducerVerify, tools.SourceRunCapture{
		CheckID: "check-verify-other", IsCheck: true, Command: "selected-check", Cwd: ".",
		Verdict: api.SourceVerdictPassed, SourceRevision: rev, SourceRootDigest: rootDigest,
	}, time.Now().UTC())
	otherHistory, err := mem.GetMessages(ctx, otherChild.ID)
	testutil.FailErr(t, "GetMessages other", err)
	return unrelatedWorker{ctx: otherCtx, child: otherChild, task: otherTask, history: otherHistory, primary: otherPrimary}
}

func assertObservedChangeInvalidatesProof(
	t *testing.T,
	workerCtx context.Context,
	mgr *session.Host,
	task *api.WorkerTask,
	childHistory []api.Message,
	curRev workercompletion.SourceRevision,
	depPath layeredfs.QualifiedPath,
) {
	t.Helper()
	proofStale := session.OverlaySourceProof(workerworkspace.WithService(workerCtx, mgr.Workers.Workspaces), task, childHistory, "selected-check", curRev)
	if status, _, _ := workercompletion.SourceEvidenceView(proofStale); status == workercompletion.EvidenceStatusSatisfied {
		t.Fatalf("expected observed dependency change via repochange.Notify to invalidate OverlaySourceProof receipt, got %s", status)
	}
	assessment, ok := session.OverlayObservationAssessmentWithWorkspaces(mgr.Workers.Workspaces, task)
	if !ok || !assessment.VerificationInvalidated || !assessment.Freshness.Stale || assessment.Freshness.StaleReason != observation.StaleReasonObservedDependencyChanged {
		t.Fatalf("unexpected overlay assessment: %+v (ok=%v)", assessment, ok)
	}
	if len(assessment.StaleSourcePaths) != 1 || assessment.StaleSourcePaths[0] != depPath {
		t.Fatalf("expected StaleSourcePaths=[%v], got %+v", depPath, assessment.StaleSourcePaths)
	}
}

func assertTrunkNoticeDeliveredOnce(t *testing.T, workerCtx context.Context, mgr *session.Host, child *api.Session) {
	t.Helper()
	// Next model turn preparation delivers trunk.changed once without interrupting in-flight calls.
	turn1, err := mgr.Runner.Preparation.Build(workerCtx, child, child.ID, promptinput.Input{}, "")
	testutil.FailErr(t, "Preparation.Build turn1", err)
	if turn1.TrunkNotice == nil || turn1.TrunkNotice.GrantsAuthority || turn1.TrunkNotice.EstablishesMergeBase {
		t.Fatalf("expected non-authoritative trunk.changed notice on turn 1, got %+v", turn1.TrunkNotice)
	}
	if len(turn1.Notices) != 1 || !strings.Contains(turn1.Notices[0], "trunk.changed") || !strings.Contains(turn1.Notices[0], "pkg/dep.py") {
		t.Fatalf("unexpected turn 1 notices: %+v", turn1.Notices)
	}
	turn2, err := mgr.Runner.Preparation.Build(workerCtx, child, child.ID, promptinput.Input{}, "")
	testutil.FailErr(t, "Preparation.Build turn2", err)
	if turn2.TrunkNotice != nil || len(turn2.Notices) != 0 {
		t.Fatalf("expected turn 2 not to duplicate consumed trunk.changed notice, got %+v", turn2.TrunkNotice)
	}
}

func TestWorkerReadAndWriteStructuredFactsWithoutSecondReadAndUnknownEvidence(t *testing.T) {
	svc := workerworkspace.New(nil, nil, nil)
	t.Cleanup(svc.Close)

	dir := t.TempDir()
	diskFile := filepath.Join(dir, "mutated.py")
	// Write mutated bytes on disk BEFORE calling ObserveReadWithFacts / RecordUpperWriteWithFacts
	// to prove neither method re-reads the file with os.ReadFile.
	testutil.FailErr(t, "WriteFile mutated", os.WriteFile(diskFile, []byte("disk-bytes-never-read-by-worker\n"), 0o644))

	// Before view capabilities are bound, a native read digest alone must not claim CompletenessComplete or LiveVisible.
	unboundObs, ok := svc.ObserveReadWithFacts("job-unbound", "sess-unbound", dir, "", nil, tools.WorkerFileObservationFacts{
		RootID:   "primary",
		RelPath:  "mutated.py",
		AbsPath:  diskFile,
		SHA256:   "sha-exact-from-read-stream",
		Size:     14,
		Complete: true,
	})
	if !ok || unboundObs.Completeness != observation.CompletenessUnknown {
		t.Fatalf("expected unbound tracker observation to report CompletenessUnknown despite native read digest, got %+v (ok=%v)", unboundObs, ok)
	}

	_, err := svc.BindViewCapabilities(
		observation.MountedAdapterViewCapabilities("job-facts", "fuselinux", true, true, true, observation.CompletenessComplete),
		"job-facts",
	)
	testutil.FailErr(t, "BindViewCapabilities job-facts", err)

	obs, ok := svc.ObserveReadWithFacts("job-facts", "sess-facts", dir, "", nil, tools.WorkerFileObservationFacts{
		RootID:   "primary",
		RelPath:  "mutated.py",
		AbsPath:  diskFile,
		SHA256:   "sha-exact-from-read-stream",
		Size:     14,
		Complete: true,
	})
	if !ok {
		t.Fatal("expected ObserveReadWithFacts to succeed")
	}
	if obs.SHA256 != "sha-exact-from-read-stream" || obs.Size != 14 || obs.Completeness != observation.CompletenessComplete || obs.Uncertain {
		t.Fatalf("expected observation to bind exact read-time facts without re-reading disk, got %+v", obs)
	}

	// RecordUpperWriteWithFacts binds exact write-time SHA256 and Size without re-reading disk,
	// and a subsequent RecordUpperWrite without facts preserves that digest.
	svc.RecordUpperWriteWithFacts("job-facts", "sess-facts", dir, "", nil, tools.WorkerFileObservationFacts{
		RootID:   "primary",
		RelPath:  "written.py",
		AbsPath:  diskFile,
		SHA256:   "sha-exact-from-write-commit",
		Size:     21,
		Layer:    "private",
		Complete: true,
	})
	svc.RecordUpperWrite("job-facts", "sess-facts", dir, "", nil, "written.py")
	tr := svc.TrackerFor("job-facts")
	writeObs, ok := tr.LatestObservationForPath(layeredfs.QualifiedPath{RootID: "primary", Rel: "written.py"})
	if !ok || writeObs.SHA256 != "sha-exact-from-write-commit" || writeObs.Size != 21 || writeObs.Layer != layeredfs.LayerPrivate {
		t.Fatalf("expected upper write observation to preserve exact write-time facts, got %+v (ok=%v)", writeObs, ok)
	}

	// When digest facts are unavailable (ObserveReadPath / Complete: false), explicit unknown/partial
	// evidence is preserved without reading the file from disk, and receipt freshness refuses to certify.
	unknownObs, ok := svc.ObserveReadPath("job-facts", "sess-facts", dir, "", nil, "mutated.py")
	if !ok {
		t.Fatal("expected ObserveReadPath to succeed")
	}
	if unknownObs.SHA256 != "" || unknownObs.Completeness != observation.CompletenessPartial || !unknownObs.Uncertain {
		t.Fatalf("expected unavailable digest facts to preserve explicit unknown/partial evidence, got %+v", unknownObs)
	}
	binding, ok := svc.BindVerificationReceipt("job-facts", "sess-facts", dir, "", "check-unknown", "ckpt-1", []string{"primary"})
	if !ok {
		t.Fatal("expected BindVerificationReceipt to succeed")
	}
	if binding.ReadAttributionCompleteness != observation.CompletenessPartial {
		t.Fatalf("expected binding completeness to downgrade to partial when observation lacks digest facts, got %s", binding.ReadAttributionCompleteness)
	}
	freshness := tr.EvaluateReceiptFreshness(binding, "ckpt-1")
	if !freshness.Stale {
		t.Fatalf("expected receipt bound to uncertain unknown-digest observation to refuse freshness certification, got %+v", freshness)
	}
}
