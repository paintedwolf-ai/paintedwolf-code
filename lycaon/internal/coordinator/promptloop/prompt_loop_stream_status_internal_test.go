package promptloop

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCompleteStreamPublishesLLMErrorWhenTheCallFails(t *testing.T) {
	projectID := eventFixtureProject(t)
	hub := events.NewMemoryHub()
	pub := &events.Publisher{Hub: hub}
	gated := newGatedFailingStreamLLM(errors.New("provider hung up"))
	loop := &PromptLoop{Deps: PromptLoopDeps{
		LLM:    gated,
		Events: pub,
		Policy: &recordingToolPolicy{},
		BuildMessages: func(_ context.Context, _ *api.Session, history []api.Message, _ *inject.CoordinatorTurnFrame) ([]api.Message, error) {
			return history, nil
		},
		CompactionConfig: staticCompactionConfig,
	}}
	sess := &api.Session{ID: "s1", ProjectID: projectID, WorkspacePath: t.TempDir()}
	subCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, unsub, err := hub.Subscribe(subCtx, events.Subscription{Project: sess.ProjectID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "hub.Subscribe failed", err)
	defer unsub()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, streamErr := modelTurn{loop}.completeStream(context.Background(), sess, "s1", []api.Message{{Role: api.MessageRoleUser, Content: "go"}}, "coordinator", "go", 0, 8, false, nil, nil)
		if streamErr == nil {
			t.Error("completeStream returned nil error for a failing stream")
		}
	}()

	<-gated.started
	hub.FlushDebounced()
	activeEvents := drainTopic(t, ch, 100*time.Millisecond)
	if len(activeEvents) != 1 {
		t.Fatalf("published %d llm events before the stream failed, want 1 active", len(activeEvents))
	}
	var active api.LLMCallEvent
	if err := json.Unmarshal(activeEvents[0].Data, &active); err != nil {
		testutil.FailErr(t, "unmarshal active llm event", err)
	}
	if active.Status != api.LLMCallStatusActive {
		t.Fatalf("pre-failure status = %q want active", active.Status)
	}

	close(gated.release)
	<-done
	hub.FlushDebounced()
	terminalEvents := drainTopic(t, ch, 100*time.Millisecond)
	if len(terminalEvents) != 1 {
		t.Fatalf("published %d llm events after the stream failed, want 1 error", len(terminalEvents))
	}
	var terminal api.LLMCallEvent
	if err := json.Unmarshal(terminalEvents[0].Data, &terminal); err != nil {
		testutil.FailErr(t, "unmarshal terminal llm event", err)
	}
	if terminal.Status != api.LLMCallStatusError {
		t.Fatalf("terminal status = %q want error", terminal.Status)
	}
	// Both events identify the same call.
	if terminal.CallID != active.CallID || terminal.CallID == "" {
		t.Fatalf("terminal call_id = %q want the active call_id %q", terminal.CallID, active.CallID)
	}
}

func TestCompleteStreamPublishesLLMActiveBeforeUserTurnCompletes(t *testing.T) {
	projectID := eventFixtureProject(t)
	hub := events.NewMemoryHub()
	pub := &events.Publisher{Hub: hub}
	gated := newGatedUsageStreamLLM(modelcall.TokenUsage{PromptTokens: 1, CompletionTokens: 1})
	loop := &PromptLoop{Deps: PromptLoopDeps{
		LLM:    gated,
		Events: pub,
		Policy: &recordingToolPolicy{},
		BuildMessages: func(_ context.Context, _ *api.Session, history []api.Message, _ *inject.CoordinatorTurnFrame) ([]api.Message, error) {
			return history, nil
		},
		CompactionConfig: staticCompactionConfig,
	}}
	sess := &api.Session{ID: "s1", ProjectID: projectID, WorkspacePath: t.TempDir()}
	subCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, unsub, err := hub.Subscribe(subCtx, events.Subscription{Project: sess.ProjectID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "hub.Subscribe failed", err)
	defer unsub()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, err := modelTurn{loop}.completeStream(context.Background(), sess, "s1", []api.Message{{Role: api.MessageRoleUser, Content: "go"}}, "coordinator", "go", 0, 8, false, nil, nil)
		testutil.FailErr(t, "completeStream failed", err)
	}()

	// Stream entry confirms the active event was published.
	<-gated.started
	hub.FlushDebounced()
	active := drainTopic(t, ch, 100*time.Millisecond)
	if len(active) != 1 {
		t.Fatalf("published %d llm events before stream completed, want 1 active", len(active))
	}
	var ev api.LLMCallEvent
	if err := json.Unmarshal(active[0].Data, &ev); err != nil {
		testutil.FailErr(t, "unmarshal active llm event", err)
	}
	if ev.Status != api.LLMCallStatusActive {
		t.Fatalf("pre-completion status = %q want active", ev.Status)
	}
	if ev.CoordinatorLoop == nil || !ev.CoordinatorLoop.Guarded {
		t.Fatalf("user turn coordinator_loop = %+v want guarded", ev.CoordinatorLoop)
	}
	if ev.CoordinatorLoop.HostTurn {
		t.Fatalf("user turn host_turn = true want false")
	}

	close(gated.release)
	<-done
	hub.FlushDebounced()
	finished := drainTopic(t, ch, 100*time.Millisecond)
	if len(finished) != 1 {
		t.Fatalf("published %d llm events after stream completed, want 1 ok", len(finished))
	}
	if err := json.Unmarshal(finished[0].Data, &ev); err != nil {
		testutil.FailErr(t, "unmarshal ok llm event", err)
	}
	if ev.Status != api.LLMCallStatusOK {
		t.Fatalf("post-completion status = %q want ok", ev.Status)
	}
}

func TestCompleteStreamPublishesLLMForCoordinatorTurns(t *testing.T) {
	projectID := eventFixtureProject(t)
	hub := events.NewMemoryHub()
	pub := &events.Publisher{Hub: hub}
	client := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{Pattern: ".", Text: "ok"}}})
	loop := &PromptLoop{Deps: PromptLoopDeps{
		LLM:    client,
		Events: pub,
		Policy: &recordingToolPolicy{},
		BuildMessages: func(_ context.Context, _ *api.Session, history []api.Message, _ *inject.CoordinatorTurnFrame) ([]api.Message, error) {
			return history, nil
		},
		CompactionConfig: staticCompactionConfig,
	}}
	sess := &api.Session{ID: "s1", ProjectID: projectID, WorkspacePath: t.TempDir()}
	worker := &api.Session{ID: "w1", ProjectID: projectID, WorkspacePath: t.TempDir(), ParentSessionID: "s1"}
	history := []api.Message{{Role: api.MessageRoleUser, Content: "go"}}

	subCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, unsub, err := hub.Subscribe(subCtx, events.Subscription{Project: sess.ProjectID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "hub.Subscribe failed", err)
	defer unsub()

	// Worker turns do not publish session LLM events.
	if _, _, err := (modelTurn{loop}).completeStream(context.Background(), worker, "w1", history, "implementer", "go", 0, 8, false, nil, nil); err != nil {
		testutil.FailErr(t, "worker completeStream failed", err)
	}
	hub.FlushDebounced()
	drainLLM := drainTopic(t, ch, 50*time.Millisecond)
	if len(drainLLM) != 0 {
		t.Fatalf("worker turn published %d llm events, want 0", len(drainLLM))
	}

	// Coordinator turns publish usage and loop progress.
	if _, _, err := (modelTurn{loop}).completeStream(context.Background(), sess, "s1", history, "coordinator", "go", 0, 8, false, nil, nil); err != nil {
		testutil.FailErr(t, "non-host coordinator completeStream failed", err)
	}
	hub.FlushDebounced()
	plain := drainTopic(t, ch, 50*time.Millisecond)
	if len(plain) != 1 {
		t.Fatalf("non-host coordinator turn published %d llm events, want 1", len(plain))
	}
	var plainEv api.LLMCallEvent
	if err := json.Unmarshal(plain[0].Data, &plainEv); err != nil {
		testutil.FailErr(t, "unmarshal non-host llm event", err)
	}
	if plainEv.Status != api.LLMCallStatusOK {
		t.Fatalf("non-host status = %q want ok", plainEv.Status)
	}
	if plainEv.CoordinatorLoop == nil || !plainEv.CoordinatorLoop.Guarded {
		t.Fatalf("non-host coordinator_loop = %+v want guarded", plainEv.CoordinatorLoop)
	}
	if plainEv.CoordinatorLoop.HostTurn {
		t.Fatalf("non-host host_turn = true want false")
	}
	if plainEv.CoordinatorLoop.Iteration != 1 || plainEv.CoordinatorLoop.MaxIterations != 8 {
		t.Fatalf("non-host iteration = %d/%d want 1/8", plainEv.CoordinatorLoop.Iteration, plainEv.CoordinatorLoop.MaxIterations)
	}
	if plainEv.Tokens.ContextWindow != compaction.DefaultCompactionConfig().ModelContextWindow {
		t.Fatalf("context_window = %d want %d (model window for the ring)", plainEv.Tokens.ContextWindow, compaction.DefaultCompactionConfig().ModelContextWindow)
	}
	if plainEv.Tokens.CompactionThreshold <= 0 {
		t.Fatalf("compaction_threshold = %d want > 0", plainEv.Tokens.CompactionThreshold)
	}

	if _, _, err := (modelTurn{loop}).completeStream(context.Background(), sess, "s1", history, "coordinator", "go", 0, 8, true, nil, nil); err != nil {
		testutil.FailErr(t, "host first iteration completeStream failed", err)
	}
	hub.FlushDebounced()
	firstIter := drainTopic(t, ch, 50*time.Millisecond)
	if len(firstIter) != 1 {
		t.Fatalf("host first iteration published %d llm events, want 1 (active+ok coalesce per session)", len(firstIter))
	}
	var firstEv api.LLMCallEvent
	if err := json.Unmarshal(firstIter[0].Data, &firstEv); err != nil {
		testutil.FailErr(t, "unmarshal first-iteration llm event", err)
	}
	if firstEv.Status != api.LLMCallStatusOK {
		t.Fatalf("first iteration status = %q want ok", firstEv.Status)
	}
	if firstEv.CoordinatorLoop == nil || firstEv.CoordinatorLoop.Iteration != 1 {
		t.Fatalf("first iteration coordinator_loop = %+v want iteration 1", firstEv.CoordinatorLoop)
	}

	if _, _, err := (modelTurn{loop}).completeStream(context.Background(), sess, "s1", history, "coordinator", "go", 1, 8, true, nil, nil); err != nil {
		testutil.FailErr(t, "host completeStream failed", err)
	}
	hub.FlushDebounced()
	hostLLM := drainTopic(t, ch, 50*time.Millisecond)
	if len(hostLLM) != 1 {
		t.Fatalf("host turn published %d llm events, want 1 (active+ok coalesce per session)", len(hostLLM))
	}
	if hostLLM[0].Topic != api.EventTopicLLM {
		t.Fatalf("topic = %q want llm", hostLLM[0].Topic)
	}
	var ev api.LLMCallEvent
	if err := json.Unmarshal(hostLLM[0].Data, &ev); err != nil {
		testutil.FailErr(t, "unmarshal llm event", err)
	}
	if ev.Status != api.LLMCallStatusOK {
		t.Fatalf("status = %q want ok", ev.Status)
	}
	if ev.CoordinatorLoop == nil || !ev.CoordinatorLoop.HostTurn {
		t.Fatalf("llm event missing coordinator_loop: %+v", ev)
	}
	if !ev.CoordinatorLoop.Guarded {
		t.Fatalf("host coordinator_loop guarded = false want true")
	}
}
