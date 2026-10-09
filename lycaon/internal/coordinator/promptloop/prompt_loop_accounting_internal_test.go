package promptloop

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/cost"
	"github.com/lycaon/lycaon/internal/cost/costtest"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCompleteStreamVoidsReceiptWhenProviderRejectsBeforeStreaming(t *testing.T) {
	tracker := costtest.NewTracker(t, nil)
	providerErr := errors.New("request rejected before generation")
	loop := NewPromptLoop(PromptLoopDeps{
		Model: ModelDeps{
			LLM:  &immediateFailingStreamLLM{err: providerErr},
			Cost: tracker,
		},
		Context: ContextDeps{
			BuildMessages: func(_ context.Context, _ *api.Session, history []api.Message, _ *inject.CoordinatorTurnFrame) ([]api.Message, error) {
				return history, nil
			},
		},
	})
	sess := &api.Session{ID: "s1", ProjectID: "project-1"}

	_, _, err := loop.Model.completeStream(t.Context(), sess, sess.ID, []api.Message{{Role: api.MessageRoleUser, Content: "go"}}, "coordinator", "go", 0, 8, false, nil, nil)
	if !errors.Is(err, providerErr) {
		t.Fatalf("completeStream error = %v want provider rejection", err)
	}
	summary, err := tracker.Summary(t.Context(), api.CostScopeSession, sess.ID, "")
	testutil.FailErr(t, "summarize voided receipt", err)
	if summary.UnknownCalls != 0 {
		t.Fatalf("unknown calls = %d want 0 for request rejected before generation", summary.UnknownCalls)
	}
}

func TestCompleteStreamDoesNotFailWhenAccountingIsUnavailable(t *testing.T) {
	base := costtest.NewTracker(t, nil)
	loop := NewPromptLoop(PromptLoopDeps{
		Model: ModelDeps{
			LLM: &usageStreamLLM{usage: modelcall.TokenUsage{PromptTokens: 4, CompletionTokens: 2}},
			Cost: accountingFailureTracker{
				CostTracker: base,
				err:         errors.New("ledger unavailable"),
			},
		},
		Context: ContextDeps{
			BuildMessages: func(_ context.Context, _ *api.Session, history []api.Message, _ *inject.CoordinatorTurnFrame) ([]api.Message, error) {
				return history, nil
			},
		},
	})
	sess := &api.Session{ID: "s1", ProjectID: "project-1"}

	completion, _, err := loop.Model.completeStream(t.Context(), sess, sess.ID, []api.Message{{Role: api.MessageRoleUser, Content: "go"}}, "coordinator", "go", 0, 8, false, nil, nil)
	testutil.FailErr(t, "completeStream", err)
	if completion == nil || completion.Content != "ok" {
		t.Fatalf("completion = %+v want successful model output", completion)
	}
}

func TestCompleteStreamRecordsUsage(t *testing.T) {
	projectID := eventFixtureProject(t)
	tracker := costtest.NewTracker(t, nil)
	hub := events.NewMemoryHub()
	pub := &events.Publisher{Hub: hub}
	loop := NewPromptLoop(PromptLoopDeps{
		Model: ModelDeps{
			LLM:  &usageStreamLLM{usage: modelcall.TokenUsage{PromptTokens: 3, CompletionTokens: 2}},
			Cost: tracker,
		},
		Projection: ProjectionDeps{
			Events: pub,
		},
		Context: ContextDeps{
			Policy: &recordingToolPolicy{},
			BuildMessages: func(_ context.Context, _ *api.Session, history []api.Message, _ *inject.CoordinatorTurnFrame) ([]api.Message, error) {
				return history, nil
			},
		},
	})
	sess := &api.Session{ID: "s1", ProjectID: projectID, WorkspacePath: t.TempDir()}
	if _, _, err := loop.Model.completeStream(context.Background(), sess, "s1", []api.Message{{Role: api.MessageRoleUser, Content: "go"}}, "coordinator", "go", 0, 8, false, nil, nil); err != nil {
		testutil.FailErr(t, "modelTurn{loop}.completeStream failed", err)
	}
	summary, err := tracker.Summary(context.Background(), api.CostScopeSession, "s1", "")
	testutil.FailErr(t, "tracker.Summary failed", err)
	if summary.Coordinator.TokenTotals.Prompt != 3 {
		t.Fatalf("prompt tokens = %d want 3", summary.Coordinator.TokenTotals.Prompt)
	}
}

func TestRecordUsagePublishesRootScopedRollupForWorkerTurns(t *testing.T) {
	projectID := eventFixtureProject(t)
	tracker := costtest.NewTracker(t, nil)
	hub := events.NewMemoryHub()
	loop := NewPromptLoop(PromptLoopDeps{
		Model: ModelDeps{
			Cost: tracker,
		},
		Projection: ProjectionDeps{
			Events: &events.Publisher{Hub: hub},
		},
		Context: ContextDeps{
			RootSessionID: func(context.Context, string) string {
				return "root-1"
			},
		},
	})
	worker := &api.Session{ID: "w1", ProjectID: projectID, ParentSessionID: "root-1"}

	subCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, unsub, err := hub.Subscribe(subCtx, events.Subscription{Project: projectID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "hub.Subscribe failed", err)
	defer unsub()

	testutil.FailErr(t, "recordUsage", loop.Model.recordUsage(
		context.Background(), worker, "call-1", "mock", "m", modelcall.CompletionRequest{},
		&modelcall.Completion{Usage: modelcall.TokenUsage{PromptTokens: 5, CompletionTokens: 2}}))
	hub.FlushDebounced()

	deadline := time.After(time.Second)
	for {
		select {
		case env := <-ch:
			if env.Topic != api.EventTopicCost {
				continue
			}
			var ev api.CostEvent
			testutil.FailErr(t, "unmarshal cost event", json.Unmarshal(env.Data, &ev))
			if ev.SessionID != "root-1" {
				t.Fatalf("cost event session = %q want root-1 (worker rollup targets the root chat)", ev.SessionID)
			}
			if ev.Workers.TokenTotals.Prompt != 5 || ev.Workers.TokenTotals.Completion != 2 {
				t.Fatalf("workers totals = %+v", ev.Workers.TokenTotals)
			}
			return
		case <-deadline:
			t.Fatal("no cost event published for worker turn")
		}
	}
}

func TestRecordAbandonedTurnUsageSurvivesCancellation(t *testing.T) {
	tracker := costtest.NewTracker(t, nil)
	loop := NewPromptLoop(PromptLoopDeps{
		Model: ModelDeps{
			Cost: tracker,
		},
	})
	sess := &api.Session{ID: "s1", ProjectID: "proj-1"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	loop.Model.recordAbandonedTurnUsage(ctx, sess, "call-1", "mock", "m", modelcall.CompletionRequest{},
		&modelcall.Completion{Usage: modelcall.TokenUsage{PromptTokens: 4, CompletionTokens: 1}})

	summary, err := tracker.Summary(context.Background(), api.CostScopeSession, "s1", "")
	testutil.FailErr(t, "tracker.Summary failed", err)
	if summary.TokenTotals.Prompt != 4 || summary.TokenTotals.Completion != 1 {
		t.Fatalf("billed partial usage dropped on cancellation: %+v", summary.TokenTotals)
	}
}

func TestRecordUsageMeasuresInterruptedTurnWithoutProviderUsage(t *testing.T) {
	tracker := costtest.NewTracker(t, nil)
	loop := NewPromptLoop(PromptLoopDeps{
		Model: ModelDeps{
			Cost: tracker,
		},
	})
	sess := &api.Session{ID: "s1", ProjectID: "proj-1"}
	req := modelcall.CompletionRequest{Messages: []api.Message{{Role: api.MessageRoleUser, Content: strings.Repeat("prompt ", 100)}}}
	delivered := &modelcall.Completion{Content: strings.Repeat("answer ", 40)}

	testutil.FailErr(t, "recordUsage", loop.Model.recordUsage(
		context.Background(), sess, "call-1", "mock", "m", req, delivered))

	summary, err := tracker.Summary(context.Background(), api.CostScopeSession, "s1", "")
	testutil.FailErr(t, "tracker.Summary failed", err)
	if summary.TokenTotals.Prompt == 0 || summary.TokenTotals.Completion == 0 {
		t.Fatalf("interrupted turn recorded no tokens: %+v", summary.TokenTotals)
	}
	if summary.UnknownCalls != 0 {
		t.Fatalf("unknown_calls = %d want 0 (the call was measured, not lost)", summary.UnknownCalls)
	}
	if summary.HostMeasuredTokens != summary.TokenTotals.Prompt+summary.TokenTotals.Completion {
		t.Fatalf("host_measured_tokens = %d want all %d tokens disclosed as host-measured",
			summary.HostMeasuredTokens, summary.TokenTotals.Prompt+summary.TokenTotals.Completion)
	}
}

func TestRecordUsageLeavesEmptyTurnUnreported(t *testing.T) {
	tracker := costtest.NewTracker(t, nil)
	loop := NewPromptLoop(PromptLoopDeps{
		Model: ModelDeps{
			Cost: tracker,
		},
	})
	sess := &api.Session{ID: "s1", ProjectID: "proj-1"}
	testutil.FailErr(t, "BeginCall", tracker.BeginCall(context.Background(), cost.UsageEvent{
		ID: "call-1", SessionID: "s1", ProjectID: "proj-1", ProviderID: "mock", Model: "m",
		Caller: cost.CallerCoordinator,
	}))

	testutil.FailErr(t, "recordUsage", loop.Model.recordUsage(
		context.Background(), sess, "call-1", "mock", "m", modelcall.CompletionRequest{}, nil))

	summary, err := tracker.Summary(context.Background(), api.CostScopeSession, "s1", "")
	testutil.FailErr(t, "tracker.Summary failed", err)
	if summary.UnknownCalls != 1 || summary.UnknownChargedCalls != 1 {
		t.Fatalf("unknown = %d charged = %d want 1/1", summary.UnknownCalls, summary.UnknownChargedCalls)
	}
}

func TestRecordUsageRecordsFallbackSelection(t *testing.T) {
	tracker := costtest.NewTracker(t, nil)
	loop := NewPromptLoop(PromptLoopDeps{
		Model: ModelDeps{
			Cost: tracker,
		},
	})
	sess := &api.Session{ID: "s1", ProjectID: "proj-1"}
	testutil.FailErr(t, "BeginCall", tracker.BeginCall(t.Context(), cost.UsageEvent{
		ID: "call-1", SessionID: sess.ID, ProjectID: sess.ProjectID,
		Caller: cost.CallerCoordinator,
	}))

	testutil.FailErr(t, "recordUsage", loop.Model.recordUsage(
		t.Context(), sess, "call-1", "mock", "mock", modelcall.CompletionRequest{},
		&modelcall.Completion{Content: "fixture", Usage: modelcall.TokenUsage{PromptTokens: 4, CompletionTokens: 2}, Fallback: true}))

	summary, err := tracker.Summary(t.Context(), api.CostScopeSession, sess.ID, "")
	testutil.FailErr(t, "Summary", err)
	if summary.UnknownCalls != 0 || summary.TokenTotals.Prompt != 4 || summary.TokenTotals.Completion != 2 {
		t.Fatalf("fallback summary = %+v", summary)
	}
}

func TestWorkerContextUsage(t *testing.T) {
	loop := NewPromptLoop(PromptLoopDeps{
		Model: ModelDeps{
			CompactionConfig: staticCompactionConfig,
		},
	})
	if got := loop.Nudges.workerContextUsage(context.Background(), nil, 0); got != nil {
		t.Fatalf("workerContextUsage(0) = %+v want nil (no tokens yet)", got)
	}
	usage := loop.Nudges.workerContextUsage(context.Background(), nil, 1234)
	if usage == nil {
		t.Fatal("workerContextUsage(1234) = nil want populated")
	}
	if usage.PromptTokens != 1234 {
		t.Fatalf("PromptTokens = %d want 1234", usage.PromptTokens)
	}
	if usage.Window != compaction.DefaultCompactionConfig().ModelContextWindow {
		t.Fatalf("Window = %d want %d", usage.Window, compaction.DefaultCompactionConfig().ModelContextWindow)
	}
	if usage.CompactionThreshold <= 0 {
		t.Fatalf("CompactionThreshold = %d want > 0", usage.CompactionThreshold)
	}

	noCfg := (NewPromptLoop(PromptLoopDeps{})).workerContextUsage(context.Background(), nil, 50)
	if noCfg == nil || noCfg.PromptTokens != 50 || noCfg.Window != 0 {
		t.Fatalf("workerContextUsage without compaction config = %+v want prompt 50, window 0", noCfg)
	}
}

func TestResolveUsageMetaPrefersCompletionFields(t *testing.T) {
	loop := NewPromptLoop(PromptLoopDeps{})
	pid, model := loop.Model.resolveUsageMeta(&api.Session{}, &modelcall.Completion{
		ProviderID: "openai",
		Model:      "gpt-4",
	})
	if pid != "openai" || model != "gpt-4" {
		t.Fatalf("meta = %q %q", pid, model)
	}
}

func TestResolveUsageMetaUsesRouterSelection(t *testing.T) {
	// Empty completion metadata uses the routed selection.
	policy := llm.NewInMemoryPolicyStore(llm.ModelPolicy{
		Coordinator: llm.ModelRef{ProviderID: "openai", Model: "gpt-4o"},
	})
	svc := &llm.Service{Registry: &llm.Registry{}, Router: llm.NewStaticModelRouter(policy)}
	loop := NewPromptLoop(PromptLoopDeps{
		Model: ModelDeps{
			LLMService: svc,
		},
	})
	pid, model := loop.Model.resolveUsageMeta(&api.Session{}, &modelcall.Completion{})
	if pid != "openai" || model != "gpt-4o" {
		t.Fatalf("meta = %q %q", pid, model)
	}
}

func TestPromptLoopUsageCallerWorker(t *testing.T) {
	if got := promptLoopUsageCaller(&api.Session{ParentSessionID: "parent"}); got != cost.CallerWorker {
		t.Fatalf("caller = %q want worker", got)
	}
}
