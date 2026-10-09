package session

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type sessionStatusFailStore struct {
	*store.Memory
	err error
}

type alwaysWithholdPromptLLM struct{}

func (alwaysWithholdPromptLLM) Complete(context.Context, modelcall.CompletionRequest) (*modelcall.Completion, error) {
	return nil, &llm.ModelRequestSecretWithheldError{Guidance: "continue without it"}
}

func (alwaysWithholdPromptLLM) Stream(context.Context, modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	return nil, &llm.ModelRequestSecretWithheldError{Guidance: "continue without it"}
}

func (s *sessionStatusFailStore) SetSessionStatus(context.Context, string, api.SessionStatus) error {
	return s.err
}

func TestFinishPromptExecutionPropagatesIdlePersistenceFailure(t *testing.T) {
	ctx := context.Background()
	wantErr := errors.New("status store unavailable")
	st := &sessionStatusFailStore{Memory: store.NewMemory(), err: wantErr}
	mgr := NewHost(st, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	sess, err := st.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	testutil.FailErr(t, "mark visible turn busy", st.Memory.SetSessionStatus(ctx, sess.ID, api.SessionStatusBusy))
	mgr.Runner.Settlement.Begin(sess.ID, "")

	testutil.FailErr(t, "finish prompt execution", mgr.Runner.Settlement.Finish(ctx, sess.ID, true, false, ""))
	err = mgr.Runner.Settlement.Drain(ctx, sess.ID)
	if !errors.Is(err, wantErr) {
		t.Fatalf("finish error = %v, want idle persistence failure", err)
	}
}

func TestFinishPromptExecutionPropagatesTopologyReportFailure(t *testing.T) {
	ctx := context.Background()
	wantErr := errors.New("topology store unavailable")
	st := store.NewMemory()
	mgr := NewHost(st, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	view := &recordingWorkflowView{topologyErr: wantErr}
	workflowFixture1 := view
	mgr.SetWorkflowDomains(&WorkflowDomains{Runs: workflowFixture1, Policy: workflowFixture1, Ambient: workflowFixture1, Blueprints: workflowFixture1, Batch: workflowFixture1, Slash: workflowFixture1, Requests: workflowFixture1, Feedback: workflowFixture1, Transcript: workflowFixture1, Asks: workflowFixture1, Fanout: workflowFixture1, Phases: workflowFixture1, Reports: workflowFixture1, Recovery: workflowFixture1, Cleanup: workflowFixture1})
	sess, err := st.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	err = mgr.Runner.Settlement.Finish(ctx, sess.ID, false, false, "committed-closeout")
	if view.closeoutID != "committed-closeout" {
		t.Fatalf("delivery lost committed message: %q", view.closeoutID)
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("finish error = %v, want topology report failure", err)
	}
}

func TestFinishPromptExecutionReconcilesWorkflowCompletion(t *testing.T) {
	ctx := t.Context()
	st := store.NewMemory()
	view := &recordingWorkflowView{}
	mgr := NewHost(st, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	workflowFixture2 := view
	mgr.SetWorkflowDomains(&WorkflowDomains{Runs: workflowFixture2, Policy: workflowFixture2, Ambient: workflowFixture2, Blueprints: workflowFixture2, Batch: workflowFixture2, Slash: workflowFixture2, Requests: workflowFixture2, Feedback: workflowFixture2, Transcript: workflowFixture2, Asks: workflowFixture2, Fanout: workflowFixture2, Phases: workflowFixture2, Reports: workflowFixture2, Recovery: workflowFixture2, Cleanup: workflowFixture2})
	sess, err := st.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	testutil.FailErr(t, "finish prompt execution", mgr.Runner.Settlement.Finish(ctx, sess.ID, false, true, ""))
	if len(view.calls) == 0 || view.calls[0] != "ReconcileTurnCompletion" {
		t.Fatalf("workflow turn-end calls = %v want completion reconciliation first", view.calls)
	}
}

func TestFinishPromptExecutionKeepsUserTurnBusyAcrossHostContinuation(t *testing.T) {
	ctx := t.Context()
	st := store.NewMemory()
	mgr := NewHost(st, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	hub := events.NewMemoryHub()
	mgr.SetEventPublisher(&events.Publisher{Hub: hub})
	sess, err := st.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	testutil.FailErr(t, "mark visible turn busy", st.SetSessionStatus(ctx, sess.ID, api.SessionStatusBusy))
	eventCh, unsubscribe, err := hub.Subscribe(ctx, events.Subscription{Project: sess.ProjectID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "subscribe to session events", err)
	t.Cleanup(unsubscribe)

	mgr.Runner.Settlement.Begin(sess.ID, "")
	mgr.Coordinator.Runtime.CoordinatorLoop().MarkWaitCalled(sess.ID)
	testutil.FailErr(t, "finish waiting prompt", mgr.Runner.Settlement.Finish(ctx, sess.ID, false, false, ""))
	afterWait, err := st.Get(ctx, sess.ID)
	testutil.FailErr(t, "read session after wait", err)
	if afterWait.Status != api.SessionStatusBusy {
		t.Fatalf("status after wait = %q want busy until host continuation settles", afterWait.Status)
	}
	assertNoSessionIdleEvent(t, eventCh)

	loop := mgr.Coordinator.Runtime.CoordinatorLoop()
	finishExecution := loop.BeginPromptExecution(t.Context(), sess.ID)
	loop.Nudge(ctx, sess.ID, anchor.PhaseAdvanced, "", "", anchor.Envelope{})
	mgr.Runner.Settlement.Begin(sess.ID, "")
	testutil.FailErr(t, "finish terminal host prompt", mgr.Runner.Settlement.Finish(ctx, sess.ID, false, true, ""))
	deferred, err := st.Get(ctx, sess.ID)
	testutil.FailErr(t, "read session before queued wake drain", err)
	if deferred.Status != api.SessionStatusBusy {
		t.Fatalf("status before queued wake drain = %q want busy", deferred.Status)
	}
	assertNoSessionIdleEvent(t, eventCh)

	finishExecution()
	testutil.FailErr(t, "drain queued wake", mgr.Runner.Settlement.Drain(ctx, sess.ID))
	if disposition := awaitIdleDisposition(t, eventCh); disposition != api.SessionIdleDispositionCompleted {
		t.Fatalf("terminal disposition = %q want completed", disposition)
	}
	settled, err := st.Get(ctx, sess.ID)
	testutil.FailErr(t, "read settled session after queued wake drain", err)
	if settled.Status != api.SessionStatusIdle {
		t.Fatalf("status after terminal host prompt = %q want idle", settled.Status)
	}
}

func assertNoSessionIdleEvent(t *testing.T, eventCh <-chan api.EventEnvelope) {
	t.Helper()
	for {
		select {
		case envelope := <-eventCh:
			if envelope.Topic != api.EventTopicSession {
				continue
			}
			var event api.SessionEvent
			testutil.FailErr(t, "decode session event", json.Unmarshal(envelope.Data, &event))
			if event.Status == api.SessionStatusIdle {
				t.Fatalf("intermediate continuation published terminal session event: %+v", event)
			}
		default:
			return
		}
	}
}

func TestFinishPromptExecutionKeepsUserTurnBusyWhileWaitIsArmed(t *testing.T) {
	ctx := t.Context()
	st := store.NewMemory()
	mgr := NewHost(st, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	sess, err := st.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	testutil.FailErr(t, "mark visible turn busy", st.SetSessionStatus(ctx, sess.ID, api.SessionStatusBusy))

	loop := mgr.Coordinator.Runtime.CoordinatorLoop()
	loop.EnterSleep(
		ctx,
		sess.ID,
		time.Now().UTC().Add(time.Minute),
		"process in flight",
		[]loopwake.WaitTrigger{loopwake.WaitTriggerProcessDone},
		[]string{"command-1"},
		loopwake.SleepMoverHost,
	)
	loop.MarkWaitCalled(sess.ID)
	mgr.Runner.Settlement.Begin(sess.ID, "")
	testutil.FailErr(t, "finish waiting prompt", mgr.Runner.Settlement.Finish(ctx, sess.ID, false, true, ""))

	waiting, err := st.Get(ctx, sess.ID)
	testutil.FailErr(t, "read waiting session", err)
	if waiting.Status != api.SessionStatusBusy {
		t.Fatalf("status while wait armed = %q want busy", waiting.Status)
	}
}

func TestFinishPromptExecutionPropagatesCompletionReconciliationFailure(t *testing.T) {
	ctx := t.Context()
	wantErr := errors.New("workflow completion unavailable")
	st := store.NewMemory()
	mgr := NewHost(st, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	workflowFixture3 := &recordingWorkflowView{completionErr: wantErr}
	mgr.SetWorkflowDomains(&WorkflowDomains{Runs: workflowFixture3, Policy: workflowFixture3, Ambient: workflowFixture3, Blueprints: workflowFixture3, Batch: workflowFixture3, Slash: workflowFixture3, Requests: workflowFixture3, Feedback: workflowFixture3, Transcript: workflowFixture3, Asks: workflowFixture3, Fanout: workflowFixture3, Phases: workflowFixture3, Reports: workflowFixture3, Recovery: workflowFixture3, Cleanup: workflowFixture3})
	sess, err := st.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	err = mgr.Runner.Settlement.Finish(ctx, sess.ID, false, true, "")
	if !errors.Is(err, wantErr) {
		t.Fatalf("finish error = %v, want completion reconciliation failure", err)
	}
}

func TestSettleDeferredUserTurnDefersWhileSessionLaneOccupied(t *testing.T) {
	ctx := t.Context()
	st := store.NewMemory()
	mgr := NewHost(st, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	sess, err := st.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	testutil.FailErr(t, "mark visible turn busy", st.SetSessionStatus(ctx, sess.ID, api.SessionStatusBusy))
	mgr.Runner.Settlement.Defer(sess.ID, api.SessionIdleDispositionCompleted)

	lane := mgr.Runner.Execution.Prompt.Acquire(sess.ID)
	lane.Lock()
	testutil.FailErr(t, "settle occupied session lane", mgr.Runner.Settlement.SettlePending(ctx, sess.ID))
	whileOccupied, err := st.Get(ctx, sess.ID)
	testutil.FailErr(t, "read occupied session", err)
	if whileOccupied.Status != api.SessionStatusBusy {
		t.Fatalf("status while session lane is occupied = %q, want busy", whileOccupied.Status)
	}
	lane.Unlock()

	testutil.FailErr(t, "settle released session lane", mgr.Runner.Settlement.SettlePending(ctx, sess.ID))
	settled, err := st.Get(ctx, sess.ID)
	testutil.FailErr(t, "read settled session", err)
	if settled.Status != api.SessionStatusIdle {
		t.Fatalf("status after session lane release = %q, want idle", settled.Status)
	}
}

func TestSettleDeferredUserTurnDropsSettlementDuringStop(t *testing.T) {
	ctx := t.Context()
	st := store.NewMemory()
	mgr := NewHost(st, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	sess, err := st.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	mgr.Runner.Settlement.Defer(sess.ID, api.SessionIdleDispositionCompleted)

	flight, leader := mgr.Chats.Gate.Begin(sess.ID)
	if !leader {
		t.Fatal("expected to lead session stop")
	}
	testutil.FailErr(t, "settle stopping session", mgr.Runner.Settlement.SettlePending(ctx, sess.ID))
	if mgr.Runner.Settlement.Pending(sess.ID) {
		t.Fatal("expected stop to discard deferred settlement")
	}
	mgr.Chats.Gate.Finish(sess.ID, flight, nil)
}

func TestTurnEndDispositionDistinguishesFailure(t *testing.T) {
	mgr := NewHost(store.NewMemory(), Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	if got := mgr.Runner.Settlement.Disposition(true); got != api.SessionIdleDispositionTurnError {
		t.Fatalf("failed turn disposition = %q", got)
	}
	if got := mgr.Runner.Settlement.Disposition(false); got != api.SessionIdleDispositionCompleted {
		t.Fatalf("successful turn disposition = %q", got)
	}
}

func TestTurnEndDispositionReportsShutdownAsInterrupted(t *testing.T) {
	mgr := NewHost(store.NewMemory(), Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	mgr.Runner.Settlement.BeginShutdown()
	if got := mgr.Runner.Settlement.Disposition(true); got != api.SessionIdleDispositionInterrupted {
		t.Fatalf("failed turn during shutdown = %q, want interrupted", got)
	}
	if got := mgr.Runner.Settlement.Disposition(false); got != api.SessionIdleDispositionCompleted {
		t.Fatalf("successful turn during shutdown = %q, want completed", got)
	}
}

func TestPromptWithoutAssistantReturnsNoInvalidResponse(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemory()
	mgr := NewHost(st, Models{Client: alwaysWithholdPromptLLM{}, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	mgr.SetDataDir(t.TempDir())
	sess, err := st.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	resp, err := mgr.Submissions.Prompt(ctx, sess.ID, "continue without the credential")
	testutil.FailErr(t, "prompt", err)
	if resp != nil {
		t.Fatalf("response = %+v, want nil when no assistant message exists", resp)
	}
	updated, err := st.Get(ctx, sess.ID)
	testutil.FailErr(t, "get session", err)
	if updated.Status != api.SessionStatusIdle {
		t.Fatalf("session status = %q, want idle", updated.Status)
	}
}
