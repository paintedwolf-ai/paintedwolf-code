package events

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func testPublisher(t *testing.T) (*Publisher, *MemoryHub, <-chan api.EventEnvelope, func()) {
	t.Helper()
	hub := NewMemoryHub()
	pub := &Publisher{
		Hub: hub,
		SessionProject: func(_ context.Context, sessionID string) (string, bool) {
			return "84a676a3-ccd2-5967-97ed-fd384c0b9003", sessionID != ""
		},
	}
	ctx := context.Background()
	ch, unsub, err := hub.Subscribe(ctx, Subscription{Project: "84a676a3-ccd2-5967-97ed-fd384c0b9003", Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "hub.Subscribe failed", err)
	return pub, hub, ch, unsub
}

func expectTopic(t *testing.T, ch <-chan api.EventEnvelope, topic api.EventTopic) api.EventEnvelope {
	t.Helper()
	select {
	case envelope := <-ch:
		if envelope.Topic != topic {
			t.Fatalf("topic = %q want %s", envelope.Topic, topic)
		}
		return envelope
	case <-time.After(time.Second):
		t.Fatalf("timeout waiting for %s event", topic)
	}
	return api.EventEnvelope{}
}

func expectTopicAfterFlush(t *testing.T, hub *MemoryHub, ch <-chan api.EventEnvelope, topic api.EventTopic) api.EventEnvelope {
	t.Helper()
	hub.FlushDebounced()
	return expectTopic(t, ch, topic)
}

func TestPublisherPublishSession(t *testing.T) {
	pub, hub, ch, unsub := testPublisher(t)
	defer unsub()
	pub.PublishSession(context.Background(), "84a676a3-ccd2-5967-97ed-fd384c0b9003", "sess-1", api.SessionStatusIdle, "hi")
	expectTopicAfterFlush(t, hub, ch, api.EventTopicSession)
}

func TestPublisherPublishLLM(t *testing.T) {
	pub, hub, ch, unsub := testPublisher(t)
	defer unsub()
	pub.PublishLLM(context.Background(), "84a676a3-ccd2-5967-97ed-fd384c0b9003", "sess-1", api.LLMCallEvent{Status: api.LLMCallStatusOK})
	expectTopicAfterFlush(t, hub, ch, api.EventTopicLLM)
}

func TestPublisherPublishOAR(t *testing.T) {
	pub, _, ch, unsub := testPublisher(t)
	defer unsub()
	want := api.OAROnFireEvent{Rule: "publisher/RULE", Anchor: "tool.pre_invoke", Effect: "warn"}
	testutil.FailErr(t, "publish OAR event", pub.PublishOAR(t.Context(), "sess-1", want))
	envelope := expectTopic(t, ch, api.EventTopicOAR)
	if envelope.Scope.Kind != api.EventScopeSession || envelope.Scope.ProjectID != "84a676a3-ccd2-5967-97ed-fd384c0b9003" || envelope.Scope.SessionID != "sess-1" {
		t.Fatalf("scope = %#v", envelope.Scope)
	}
	var got api.OAROnFireEvent
	testutil.FailErr(t, "decode OAR event", json.Unmarshal(envelope.Data, &got))
	want.SessionID = "sess-1"
	if got != want {
		t.Fatalf("event = %#v, want %#v", got, want)
	}
}

// Session identity separates turns on the project-scoped stream.
func TestPublisherPublishLLMStampsSessionID(t *testing.T) {
	pub, hub, ch, unsub := testPublisher(t)
	defer unsub()
	pub.PublishLLM(context.Background(), "84a676a3-ccd2-5967-97ed-fd384c0b9003", "sess-1", api.LLMCallEvent{Status: api.LLMCallStatusActive})
	env := expectTopicAfterFlush(t, hub, ch, api.EventTopicLLM)
	var ev api.LLMCallEvent
	if err := json.Unmarshal(env.Data, &ev); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	if ev.SessionID != "sess-1" {
		t.Fatalf("SessionID = %q, want %q", ev.SessionID, "sess-1")
	}
}

func TestPublisherPublishCost(t *testing.T) {
	pub, hub, ch, unsub := testPublisher(t)
	defer unsub()
	pub.PublishCost(context.Background(), "84a676a3-ccd2-5967-97ed-fd384c0b9003", "sess-1", api.CostEvent{
		EstimatedNanoUsd: 10_000_000,
		Coordinator:      api.CostBreakdown{EstimatedNanoUsd: 10_000_000},
		Workers:          api.CostBreakdown{},
	})
	expectTopicAfterFlush(t, hub, ch, api.EventTopicCost)
}

func TestPublisherPublishWorkflow(t *testing.T) {
	pub, _, ch, unsub := testPublisher(t)
	defer unsub()
	pub.PublishWorkflow(context.Background(), "84a676a3-ccd2-5967-97ed-fd384c0b9003", "sess-1", api.WorkflowEvent{WorkflowRunID: "run-1"})
	expectTopic(t, ch, api.EventTopicWorkflow)
}

func TestPublisherPublishGrounding(t *testing.T) {
	pub, _, ch, unsub := testPublisher(t)
	defer unsub()
	pub.PublishGrounding(context.Background(), "84a676a3-ccd2-5967-97ed-fd384c0b9003", "sess-1", "COORDINATOR_UNGROUNDED_CLAIM", "leg-1", "dep-1", false)
	expectTopic(t, ch, api.EventTopicGrounding)
}

func TestPublisherPublishProgress(t *testing.T) {
	pub, _, ch, unsub := testPublisher(t)
	defer unsub()
	pub.PublishProgress(context.Background(), "sess-1", 3)
	env := expectTopic(t, ch, api.EventTopicProgress)
	if env.Scope.Kind != api.EventScopeSession || env.Scope.ProjectID != "84a676a3-ccd2-5967-97ed-fd384c0b9003" || env.Scope.SessionID != "sess-1" {
		t.Fatalf("scope = %#v", env.Scope)
	}
}

func TestPublisherPublishGatePending(t *testing.T) {
	pub, _, ch, unsub := testPublisher(t)
	defer unsub()
	pub.PublishGatePending(context.Background(), "84a676a3-ccd2-5967-97ed-fd384c0b9003", "sess-1", api.WorkflowEvent{WorkflowRunID: "run-1", Event: "gate_pending"})
	expectTopic(t, ch, api.EventTopicWorkflow)
}

func TestPublisherPublishWorkflowPersisted(t *testing.T) {
	pub, _, ch, unsub := testPublisher(t)
	defer unsub()
	pub.PublishWorkflowPersisted(context.Background(), "84a676a3-ccd2-5967-97ed-fd384c0b9003", "sess-1", api.WorkflowEvent{WorkflowID: "wf-1"})
	expectTopic(t, ch, api.EventTopicWorkflow)
}

func TestPublisherPublishLLMAssignsCallID(t *testing.T) {
	pub, hub, ch, unsub := testPublisher(t)
	defer unsub()
	pub.PublishLLM(context.Background(), "84a676a3-ccd2-5967-97ed-fd384c0b9003", "sess-1", api.LLMCallEvent{Status: api.LLMCallStatusOK})
	hub.FlushDebounced()
	select {
	case envelope := <-ch:
		var ev api.LLMCallEvent
		if err := json.Unmarshal(envelope.Data, &ev); err != nil {
			testutil.FailErr(t, "unmarshal JSON document", err)
		}
		if ev.CallID == "" {
			t.Fatal("expected generated call_id")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}
}

// blockingSessionUI holds one session build open while another completes.
type blockingSessionUI struct {
	release chan struct{}
	entered chan struct{}
}

func (s *blockingSessionUI) ComputeSessionUI(context.Context, string) (*api.SessionUiState, error) {
	s.entered <- struct{}{}
	<-s.release
	return &api.SessionUiState{}, nil
}

type staticSessionUI struct{}

func (staticSessionUI) ComputeSessionUI(context.Context, string) (*api.SessionUiState, error) {
	return &api.SessionUiState{}, nil
}

// A delayed session build cannot replace a newer published revision.
func TestPublisherPublishSessionOrdersConcurrentBuildsByCallNotFinish(t *testing.T) {
	hub := NewMemoryHub()
	slow := &blockingSessionUI{release: make(chan struct{}), entered: make(chan struct{})}
	pub := &Publisher{Hub: hub, SessionUI: slow}
	ctx := context.Background()

	ch, unsub, err := hub.Subscribe(ctx, Subscription{Project: "84a676a3-ccd2-5967-97ed-fd384c0b9003", Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "hub.Subscribe failed", err)
	defer unsub()

	// The slow build is called first (lower revision) and blocks mid-build.
	done := make(chan struct{})
	go func() {
		pub.PublishSession(ctx, "84a676a3-ccd2-5967-97ed-fd384c0b9003", "sess-1", api.SessionStatusBusy, "")
		close(done)
	}()
	<-slow.entered

	// The fast build is called second (higher revision) and finishes first.
	pub.SessionUI = staticSessionUI{}
	pub.PublishSession(ctx, "84a676a3-ccd2-5967-97ed-fd384c0b9003", "sess-1", api.SessionStatusIdle, "done")

	// Release the stale build after the fresh revision publishes.
	close(slow.release)
	<-done

	envelope := expectTopicAfterFlush(t, hub, ch, api.EventTopicSession)
	var ev api.SessionEvent
	testutil.FailErr(t, "unmarshal session event", json.Unmarshal(envelope.Data, &ev))
	if ev.Status != api.SessionStatusIdle {
		t.Fatalf("status = %q, want %q (stale busy build must not win)", ev.Status, api.SessionStatusIdle)
	}
}

func TestPublisherNilSafe(t *testing.T) {
	var pub *Publisher
	ctx := context.Background()
	pub.PublishSession(ctx, "6083f538-41e9-5089-94c9-733e281e9b63", "s", api.SessionStatusIdle, "")
	pub.PublishBoard(ctx, "6083f538-41e9-5089-94c9-733e281e9b63", "sess-1")
	pub.PublishLLM(ctx, "6083f538-41e9-5089-94c9-733e281e9b63", "s", api.LLMCallEvent{})
	pub.PublishCost(ctx, "6083f538-41e9-5089-94c9-733e281e9b63", "s", api.CostEvent{})
	pub.PublishWorkflow(ctx, "6083f538-41e9-5089-94c9-733e281e9b63", "s", api.WorkflowEvent{})
	pub.PublishGrounding(ctx, "6083f538-41e9-5089-94c9-733e281e9b63", "s", "code", "", "", false)
	pub.PublishGatePending(ctx, "6083f538-41e9-5089-94c9-733e281e9b63", "s", api.WorkflowEvent{})
	pub.PublishWorkflowPersisted(ctx, "6083f538-41e9-5089-94c9-733e281e9b63", "s", api.WorkflowEvent{})
}

// blockingBoardSource lets a test hold one BuildView call open while another
// runs to completion, to reproduce a slow build finishing after a fast one.
type blockingBoardSource struct {
	release chan struct{}
	entered chan struct{}
}

func (s *blockingBoardSource) BuildView(_ context.Context, _, _, _ string, _ api.BoardDetailLevel) (api.BoardView, error) {
	if s.entered != nil {
		s.entered <- struct{}{}
	}
	if s.release != nil {
		<-s.release
	}
	return api.BoardView{Summary: "stale"}, nil
}

type staticBoardSource struct{ summary string }

func (s staticBoardSource) BuildView(_ context.Context, _, _, _ string, _ api.BoardDetailLevel) (api.BoardView, error) {
	return api.BoardView{Summary: s.summary}, nil
}

// A delayed board build cannot replace a newer published revision.
func TestPublisherPublishBoardOrdersConcurrentBuildsByCallNotFinish(t *testing.T) {
	hub := NewMemoryHub()
	slow := &blockingBoardSource{release: make(chan struct{}), entered: make(chan struct{})}
	pub := &Publisher{Hub: hub, Board: slow}
	ctx := context.Background()

	ch, unsub, err := hub.Subscribe(ctx, Subscription{Project: "84a676a3-ccd2-5967-97ed-fd384c0b9003", Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "hub.Subscribe failed", err)
	defer unsub()

	// The slow build is called first (lower revision) and blocks mid-build.
	done := make(chan struct{})
	go func() {
		pub.PublishBoard(ctx, "84a676a3-ccd2-5967-97ed-fd384c0b9003", "sess-1")
		close(done)
	}()
	<-slow.entered

	// The fast build is called second (higher revision) and finishes first.
	pub.Board = staticBoardSource{summary: "fresh"}
	pub.PublishBoard(ctx, "84a676a3-ccd2-5967-97ed-fd384c0b9003", "sess-1")

	// Release the stale build after the fresh revision publishes.
	close(slow.release)
	<-done

	envelope := expectTopicAfterFlush(t, hub, ch, api.EventTopicBoard)
	var ev api.BoardEvent
	testutil.FailErr(t, "unmarshal board event", json.Unmarshal(envelope.Data, &ev))
	if ev.Snapshot.Summary != "fresh" {
		t.Fatalf("summary = %q, want %q (stale late build must not win)", ev.Snapshot.Summary, "fresh")
	}
}

// Each session has its own board debounce window.
func TestPublisherPublishBoardFacetsBySession(t *testing.T) {
	hub := NewMemoryHub()
	pub := &Publisher{Hub: hub, Board: staticBoardSource{summary: "s"}}
	ctx := context.Background()

	ch, unsub, err := hub.Subscribe(ctx, Subscription{Project: "84a676a3-ccd2-5967-97ed-fd384c0b9003", Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "hub.Subscribe failed", err)
	defer unsub()

	pub.PublishBoard(ctx, "84a676a3-ccd2-5967-97ed-fd384c0b9003", "sess-1")
	pub.PublishBoard(ctx, "84a676a3-ccd2-5967-97ed-fd384c0b9003", "sess-2")
	hub.FlushDebounced()

	seen := map[string]bool{}
	for i := 0; i < 2; i++ {
		envelope := expectTopic(t, ch, api.EventTopicBoard)
		var ev api.BoardEvent
		testutil.FailErr(t, "unmarshal board event", json.Unmarshal(envelope.Data, &ev))
		seen[ev.SessionID] = true
	}
	if !seen["sess-1"] || !seen["sess-2"] {
		t.Fatalf("expected board events for both sessions, got %#v", seen)
	}
}

func TestPublisherPublishCheckpoint(t *testing.T) {
	hub := NewMemoryHub()
	pub := &Publisher{Hub: hub}
	ctx := context.Background()

	ch, unsub, err := hub.Subscribe(ctx, Subscription{Project: "84a676a3-ccd2-5967-97ed-fd384c0b9003", Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "hub.Subscribe failed", err)
	defer unsub()

	pub.PublishCheckpoint(ctx, "84a676a3-ccd2-5967-97ed-fd384c0b9003", "session-1", api.CheckpointEvent{
		ID:        "chk-1",
		SessionID: "session-1",
		Kind:      api.CheckpointKindToolApproval,
		Status:    api.CheckpointStatusPending,
		ToolApproval: &api.ToolApprovalPayload{
			Plan: api.ApprovalPlan{
				Presentation: api.ApprovalPlanPresentation{Tool: "write", Action: "Approve write"},
			},
		},
	})

	expectTopic(t, ch, api.EventTopicCheckpoint)
}

func TestPublisherPublishCheckpointImmediate(t *testing.T) {
	hub := NewMemoryHub()
	pub := &Publisher{Hub: hub}
	ctx := context.Background()

	ch, unsub, err := hub.Subscribe(ctx, Subscription{Project: "84a676a3-ccd2-5967-97ed-fd384c0b9003", Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "hub.Subscribe failed", err)
	defer unsub()

	for i := 0; i < 2; i++ {
		pub.PublishCheckpoint(ctx, "84a676a3-ccd2-5967-97ed-fd384c0b9003", "session-1", api.CheckpointEvent{
			ID:        "chk-1",
			SessionID: "session-1",
			Kind:      api.CheckpointKindToolApproval,
			Status:    api.CheckpointStatusPending,
		})
	}

	for i := 0; i < 2; i++ {
		expectTopic(t, ch, api.EventTopicCheckpoint)
	}
}

func TestPublisherPublishCheckpointPayload(t *testing.T) {
	hub := NewMemoryHub()
	pub := &Publisher{Hub: hub}
	ctx := context.Background()

	ch, unsub, err := hub.Subscribe(ctx, Subscription{Project: "84a676a3-ccd2-5967-97ed-fd384c0b9003", Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "hub.Subscribe failed", err)
	defer unsub()

	pub.PublishCheckpoint(ctx, "84a676a3-ccd2-5967-97ed-fd384c0b9003", "session-1", api.CheckpointEvent{
		ID:        "chk-1",
		SessionID: "session-1",
		Kind:      api.CheckpointKindToolApproval,
		Status:    api.CheckpointStatusPending,
		ToolApproval: &api.ToolApprovalPayload{
			Plan: api.ApprovalPlan{
				Presentation: api.ApprovalPlanPresentation{Tool: "write", Action: "Approve write"},
			},
		},
	})

	select {
	case envelope := <-ch:
		if envelope.Topic != api.EventTopicCheckpoint {
			t.Fatalf("topic = %q want checkpoint", envelope.Topic)
		}
		var ev api.CheckpointEvent
		if err := json.Unmarshal(envelope.Data, &ev); err != nil {
			testutil.FailErr(t, "unmarshal JSON document", err)
		}
		if ev.ID != "chk-1" || ev.ToolApproval == nil || ev.ToolApproval.Plan.Presentation.Tool != "write" || ev.Status != api.CheckpointStatusPending {
			t.Fatalf("event = %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for checkpoint event")
	}
}

func TestPublisherPublishCheckpointNilSafe(t *testing.T) {
	var pub *Publisher
	pub.PublishCheckpoint(context.Background(), "6083f538-41e9-5089-94c9-733e281e9b63", "s", api.CheckpointEvent{})
}

// Den renders stopped versus broken from idle_disposition alone.
func TestPublisherPublishSessionIdleCarriesDisposition(t *testing.T) {
	cases := []struct {
		name        string
		disposition api.SessionIdleDisposition
	}{
		{"normal end", api.SessionIdleDispositionCompleted},
		{"user stop", api.SessionIdleDispositionUserStopped},
		{"caught turn failure", api.SessionIdleDispositionTurnError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pub, hub, ch, unsub := testPublisher(t)
			defer unsub()
			pub.PublishSessionIdle(context.Background(), "84a676a3-ccd2-5967-97ed-fd384c0b9003", "sess-1", "hi", tc.disposition)
			envelope := expectTopicAfterFlush(t, hub, ch, api.EventTopicSession)
			var ev api.SessionEvent
			testutil.FailErr(t, "decode session event", json.Unmarshal(envelope.Data, &ev))
			if ev.Status != api.SessionStatusIdle {
				t.Fatalf("status = %q, want idle", ev.Status)
			}
			if ev.IdleDisposition != tc.disposition {
				t.Fatalf("idle_disposition = %q, want %q", ev.IdleDisposition, tc.disposition)
			}
		})
	}
}

// Idle disposition describes settled turns only.
func TestPublisherPublishSessionLeavesDispositionUnsetWhenBusy(t *testing.T) {
	pub, hub, ch, unsub := testPublisher(t)
	defer unsub()
	pub.PublishSession(context.Background(), "84a676a3-ccd2-5967-97ed-fd384c0b9003", "sess-1", api.SessionStatusBusy, "hi")
	envelope := expectTopicAfterFlush(t, hub, ch, api.EventTopicSession)
	var ev api.SessionEvent
	testutil.FailErr(t, "decode session event", json.Unmarshal(envelope.Data, &ev))
	if ev.IdleDisposition != "" {
		t.Fatalf("idle_disposition = %q, want empty on a busy event", ev.IdleDisposition)
	}
}

// A supplied call ID correlates every event for one provider call.
func TestPublishLLMPreservesCallID(t *testing.T) {
	ctx := context.Background()
	hub := NewMemoryHub()
	pub := &Publisher{Hub: hub}

	ch, unsubscribe, err := hub.Subscribe(ctx, Subscription{Project: "", Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "subscribe", err)
	defer unsubscribe()

	const callID = "call-1"
	pub.PublishLLM(ctx, "d54876ce-88f5-573c-b1e0-763252826c33", "sess-1", api.LLMCallEvent{
		CallID: callID,
		Status: api.LLMCallStatusActive,
	})

	select {
	case env := <-ch:
		var ev api.LLMCallEvent
		testutil.FailErr(t, "unmarshal", json.Unmarshal(env.Data, &ev))
		if ev.CallID != callID {
			t.Fatalf("call_id = %q, want the supplied %q", ev.CallID, callID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no llm event delivered")
	}
}
