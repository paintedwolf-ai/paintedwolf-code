package session

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/guidance/feedback"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session/postturn"
	"github.com/lycaon/lycaon/internal/session/promptinput"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/workercontext"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testtool"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/oartest"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// receiptRecorder records admitted receipt ids. Host turns mint their own operation id.
type receiptRecorder struct {
	*store.Memory
	mu  sync.Mutex
	ids []string
}

func (r *receiptRecorder) PutPromptSubmission(ctx context.Context, in store.PromptSubmission) (*store.PromptSubmission, bool, error) {
	row, created, err := r.Memory.PutPromptSubmission(ctx, in)
	if err == nil && created {
		r.mu.Lock()
		r.ids = append(r.ids, row.ID)
		r.mu.Unlock()
	}
	return row, created, err
}

func (r *receiptRecorder) admitted() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.ids...)
}

func newReceiptTestManager(t *testing.T) (*Host, *receiptRecorder) {
	t.Helper()
	recorder := &receiptRecorder{Memory: store.NewMemory()}
	registry := tools.NewStubRegistry()
	mgr := NewHost(recorder, Models{Client: llm.NewMockProvider(testMockConfig(t)), Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, registry)
	oartest.InstallCloseoutPolicy(t, mgr)
	mgr.Coordinator.Guards.SetToolMetadata(testtool.RegistryInvoker{Registry: registry})
	return mgr, recorder
}

func newReceiptTestSession(t *testing.T, mgr *Host, sessions *receiptRecorder) *api.Session {
	t.Helper()
	sess, err := sessions.Create(context.Background(), api.CreateSessionRequest{
		Posture: api.SessionPostureBuild,
	}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	_ = mgr
	return sess
}

func TestHostTurnWritesReceipt(t *testing.T) {
	mgr, sessions := newReceiptTestManager(t)
	ctx := workercontext.WithJob(context.Background(), "worker-job-1")
	sess := newReceiptTestSession(t, mgr, sessions)

	_, err := mgr.Submissions.PromptHostTurn(ctx, sess.ID, store.PromptSubmissionOriginWorkerCloseout, "closeout kick")
	testutil.FailErr(t, "run host turn", err)

	ids := sessions.admitted()
	if len(ids) != 1 {
		t.Fatalf("host turn admitted %d receipts, want 1", len(ids))
	}
	row, err := mgr.Submissions.GetPromptSubmission(ctx, ids[0])
	testutil.FailErr(t, "get host receipt", err)
	if row.Origin != store.PromptSubmissionOriginWorkerCloseout {
		t.Fatalf("receipt origin = %q, want worker_closeout", row.Origin)
	}
	if row.Status != store.PromptSubmissionComplete {
		t.Fatalf("receipt status = %q, want complete", row.Status)
	}
	var stored promptinput.Input
	testutil.FailErr(t, "decode stored input", json.Unmarshal([]byte(row.InputJSON), &stored))
	if stored.Text != "closeout kick" {
		t.Fatalf("stored input text = %q, want the kick text", stored.Text)
	}
	if stored.WorkerJobID != "worker-job-1" {
		t.Fatalf("stored worker_job_id = %q", stored.WorkerJobID)
	}
	if stored.HostSignal == nil || stored.HostSignal.Kind != api.MessageKindHostKick ||
		stored.HostSignal.ID != string(anchor.WorkerCloseout) {
		t.Fatalf("stored host signal = %+v, want worker.closeout kick", stored.HostSignal)
	}
	if !stored.ProseFinish {
		t.Fatal("worker closeout must persist ProseFinish so replay gates invocation")
	}

	msgs, err := sessions.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "get messages", err)
	var kick api.Message
	for _, msg := range msgs {
		if msg.Role == api.MessageRoleUser && msg.Origin == api.MessageOriginHost {
			kick = msg
			break
		}
	}
	if kick.ID == "" {
		t.Fatal("worker closeout must persist a host user row")
	}
	if kick.Visibility != api.MessageVisibilityInternal {
		t.Fatalf("closeout visibility = %q, want internal", kick.Visibility)
	}
	if kick.Kind != api.MessageKindHostKick || kick.HostSignalID != string(anchor.WorkerCloseout) {
		t.Fatalf("closeout row kind/signal = %q/%q", kick.Kind, kick.HostSignalID)
	}
	if kick.WorkerID != "worker-job-1" {
		t.Fatalf("closeout worker_job_id = %q", kick.WorkerID)
	}
}

func TestHostTurnGroundingRetryKeepsTools(t *testing.T) {
	mgr, sessions := newReceiptTestManager(t)
	ctx := context.Background()
	sess := newReceiptTestSession(t, mgr, sessions)

	_, err := mgr.Submissions.PromptHostTurn(ctx, sess.ID, store.PromptSubmissionOriginGroundingRetry, "retry kick")
	testutil.FailErr(t, "run grounding retry", err)

	ids := sessions.admitted()
	if len(ids) != 1 {
		t.Fatalf("grounding retry admitted %d receipts, want 1", len(ids))
	}
	row, err := mgr.Submissions.GetPromptSubmission(ctx, ids[0])
	testutil.FailErr(t, "get grounding receipt", err)
	var stored promptinput.Input
	testutil.FailErr(t, "decode stored input", json.Unmarshal([]byte(row.InputJSON), &stored))
	if stored.ProseFinish {
		t.Fatal("grounding retry must keep tools")
	}
	if stored.HostSignal == nil || stored.HostSignal.Kind != api.MessageKindHostKick ||
		stored.HostSignal.ID != string(anchor.WorkerCitationGrounding) {
		t.Fatalf("stored host signal = %+v, want worker.citation.grounding kick", stored.HostSignal)
	}
}

func TestHostLoopWakeReceiptCarriesHostSignal(t *testing.T) {
	mgr, sessions := newReceiptTestManager(t)
	ctx := context.Background()
	sess := newReceiptTestSession(t, mgr, sessions)

	_, err := mgr.Submissions.LoopWake(ctx, sess.ID)
	testutil.FailErr(t, "run loop wake", err)

	ids := sessions.admitted()
	if len(ids) != 1 {
		t.Fatalf("loop wake admitted %d receipts, want 1", len(ids))
	}
	row, err := mgr.Submissions.GetPromptSubmission(ctx, ids[0])
	testutil.FailErr(t, "get loop wake receipt", err)
	if row.Origin != store.PromptSubmissionOriginLoopWake {
		t.Fatalf("receipt origin = %q, want loop_wake", row.Origin)
	}
	var stored promptinput.Input
	testutil.FailErr(t, "decode stored input", json.Unmarshal([]byte(row.InputJSON), &stored))
	if stored.HostSignal == nil || stored.HostSignal.Kind != api.MessageKindHostLoopWake {
		t.Fatalf("stored host signal = %+v, want a loop-wake signal", stored.HostSignal)
	}
}

func TestRecoveryResolvesHostReceiptsWithoutReplay(t *testing.T) {
	mgr, sessions := newReceiptTestManager(t)
	ctx := context.Background()
	sess := newReceiptTestSession(t, mgr, sessions)

	userRow, _, err := mgr.Submissions.AdmitPrompt(ctx, sess.ID, uuid.NewString(), "user work", promptinput.Input{Text: "user work"})
	testutil.FailErr(t, "admit user prompt", err)
	hostInput, err := json.Marshal(promptinput.Input{Text: "retry kick"})
	testutil.FailErr(t, "encode host receipt fixture", err)
	hostRow, _, err := sessions.PutPromptSubmission(ctx, store.PromptSubmission{ID: uuid.NewString(), SessionID: sess.ID, ProjectID: sess.ProjectID, Origin: store.PromptSubmissionOriginGroundingRetry, InputJSON: string(hostInput)})
	testutil.FailErr(t, "admit host prompt", err)

	recovered, err := mgr.Submissions.RecoverPromptSubmissions(ctx)
	testutil.FailErr(t, "recover submissions", err)
	if len(recovered) != 1 || recovered[0] != userRow.ID {
		t.Fatalf("recovered = %v, want only the user receipt %s", recovered, userRow.ID)
	}

	resolved, err := mgr.Submissions.GetPromptSubmission(ctx, hostRow.ID)
	testutil.FailErr(t, "get host receipt", err)
	if resolved.Status != store.PromptSubmissionInterrupted {
		t.Fatalf("host receipt status = %q, want interrupted", resolved.Status)
	}
	if resolved.Error == "" {
		t.Fatal("an interrupted host receipt must record why it was not replayed")
	}
}

func newPostHookManager(t *testing.T) *Host {
	t.Helper()
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	hints, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "load hint registry", err)
	mgr, _ := newTestManager(t)
	mgr.SetRejectFormatter(guidance.NewStaticRejectFormatter(hints))
	return mgr
}

func pendingProgressMissingNudge(mgr *Host, sessionID string) bool {
	return anchor.SameInform(mgr.Coordinator.Runtime.Kicks().TakePendingKickID(sessionID), anchor.ProgressMissing)
}

type postHookWorkflowContext struct {
	wf feedback.WorkflowEvaluationContext
}

func (s postHookWorkflowContext) BuildCoordinatorTurnFrame(_ context.Context, _ string, _ *api.Session) (inject.CoordinatorTurnFrame, error) {
	runCtx := api.CoordinatorRunContext{
		WorkflowID:         s.wf.WorkflowID,
		CurrentPhase:       s.wf.CurrentPhase,
		RunStatus:          string(api.WorkflowRunStatusRunning),
		AdvanceWhenGateMet: s.wf.AdvanceWhenGateMet,
	}
	frame := inject.CoordinatorTurnFrame{RunContext: runCtx}
	frame.Runtime.PhaseExit = &inject.PhaseExitView{Kind: s.wf.PhaseExitKind}
	if s.wf.CurrentGatesKnown {
		frame.Runtime.Phases = []inject.WorkflowPhaseRow{{ID: s.wf.CurrentPhase}}
		if !s.wf.CurrentGatesPassed {
			for _, leaf := range s.wf.FailedLeaves {
				frame.Runtime.Phases[0].Gates = append(frame.Runtime.Phases[0].Gates, inject.WorkflowGateState{ID: leaf})
			}
		} else {
			frame.Runtime.Phases[0].Gates = []inject.WorkflowGateState{{ID: "ready", Satisfied: true}}
		}
	}
	return frame, nil
}

func TestProgressMissingNudgesOnDispatchWithoutPlan(t *testing.T) {
	mgr := newPostHookManager(t)
	mgr.SetProgressStore(progress.NewMemoryStore())

	mgr.Runner.PostTurn.ProgressMissing(t.Context(), "root-1", []string{"task"})
	if !pendingProgressMissingNudge(mgr, "root-1") {
		t.Fatal("expected PROGRESS_MISSING after task() without plan")
	}
}

func TestProgressMissingSkipsAfterRejectedDispatchTurn(t *testing.T) {
	mgr := newPostHookManager(t)
	mgr.SetProgressStore(progress.NewMemoryStore())

	mgr.Runner.PostTurn.ProgressMissing(t.Context(), "root-1", []string{"list_dir"})
	if pendingProgressMissingNudge(mgr, "root-1") {
		t.Fatal("plan missing nudge only fires on the turn that attempts dispatch")
	}
}

func TestProgressMissingSkipsWorkerChild(t *testing.T) {
	mgr := newPostHookManager(t)
	mem := store.NewMemory()
	ctx := t.Context()
	parent, err := mem.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, "proj-1")
	testutil.FailErr(t, "Create parent", err)
	child, err := mem.CreateChild(ctx, parent, api.SpawnChildRequest{AgentType: "implementer"})
	testutil.FailErr(t, "CreateChild", err)
	mgr.Coordinator.Context.Sessions = mem
	mgr.Runner.PostTurn = postturn.New(mem, mgr.Coordinator.Guidance)
	mgr.SetProgressStore(progress.NewMemoryStore())

	mgr.Runner.PostTurn.ProgressMissing(ctx, child.ID, []string{"task"})
	if pendingProgressMissingNudge(mgr, child.ID) {
		t.Fatal("worker children must not receive coordinator progress-missing informs")
	}
	if err := mgr.Runner.PostTurn.After(ctx, child.ID, orchestration.ProfileImplementer, []string{"task"}); err != nil {
		testutil.FailErr(t, "afterPrompt", err)
	}
	if pendingProgressMissingNudge(mgr, child.ID) {
		t.Fatal("afterPrompt must skip coordinator lifecycle on worker children")
	}
}

func TestProgressMissingSkipsSpecWorkflowArtifacts(t *testing.T) {
	mgr := newPostHookManager(t)
	mem := store.NewMemory()
	sess, err := mem.Create(t.Context(), api.CreateSessionRequest{Posture: api.SessionPostureSpec}, "proj-1")
	testutil.FailErr(t, "Create session", err)
	mgr.Coordinator.Context.Sessions = mem
	mgr.Runner.PostTurn = postturn.New(mem, mgr.Coordinator.Guidance)
	mgr.SetProgressStore(progress.NewMemoryStore())

	mgr.Runner.PostTurn.ProgressMissing(t.Context(), sess.ID, []string{"write"})
	if pendingProgressMissingNudge(mgr, sess.ID) {
		t.Fatal("spec workflow artifact writes must not require a build checklist")
	}
}

func TestWorkflowPhaseExitNudgesAfterUnrelatedToolArc(t *testing.T) {
	mgr := newPostHookManager(t)
	mem := store.NewMemory()
	sess, err := mem.Create(t.Context(), api.CreateSessionRequest{}, "proj-1")
	testutil.FailErr(t, "Create session", err)
	mgr.Coordinator.Context.Sessions = mem
	mgr.Runner.PostTurn = postturn.New(mem, mgr.Coordinator.Guidance)
	mgr.SetCoordinatorTurnFrameSource(postHookWorkflowContext{wf: feedback.WorkflowEvaluationContext{
		WorkflowID:         "example",
		CurrentPhase:       "intake",
		RunActive:          true,
		AdvanceWhenGateMet: "coordinator",
		CurrentGatesKnown:  true,
		CurrentGatesPassed: true,
		PhaseExitKind:      "proof",
	}})

	if !mgr.Runner.PostTurn.WorkflowExit(t.Context(), sess.ID, orchestration.ProfileCoordinator, []string{"read"}) {
		t.Fatal("expected phase-exit nudge")
	}
	if id := mgr.Coordinator.Runtime.Kicks().TakePendingKickID(sess.ID); !anchor.SameInform(id, anchor.PhaseExitRequired) {
		t.Fatalf("kick id = %q", id)
	}
}

func TestWorkflowPhaseExitAllowsAnotherAsk(t *testing.T) {
	mgr := newPostHookManager(t)
	mem := store.NewMemory()
	sess, err := mem.Create(t.Context(), api.CreateSessionRequest{}, "proj-1")
	testutil.FailErr(t, "Create session", err)
	mgr.Coordinator.Context.Sessions = mem
	mgr.Runner.PostTurn = postturn.New(mem, mgr.Coordinator.Guidance)
	mgr.SetCoordinatorTurnFrameSource(postHookWorkflowContext{wf: feedback.WorkflowEvaluationContext{
		WorkflowID:         "example",
		CurrentPhase:       "intake",
		RunActive:          true,
		AdvanceWhenGateMet: "coordinator",
		CurrentGatesKnown:  true,
		CurrentGatesPassed: true,
		PhaseExitKind:      "proof",
	}})

	if mgr.Runner.PostTurn.WorkflowExit(t.Context(), sess.ID, orchestration.ProfileCoordinator, []string{"ask_user"}) {
		t.Fatal("another structured ask must keep coordinator intake open")
	}
}
