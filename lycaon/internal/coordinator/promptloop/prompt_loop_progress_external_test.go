package promptloop_test

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/workerprogress"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestStreamProgressBuffersContentUntilSettle(t *testing.T) {
	st := store.NewMemory()
	deps := promptloop.StoreDeps(st)
	var live, updates int
	deps.Projection.Streams = &observedMessageStreams{MessageStreams: deps.Projection.Streams, project: func(ctx context.Context, sessionID string, msg api.Message) error {
		live++
		return st.PatchLiveProjection(ctx, sessionID, msg.ID, msg.Content, msg.ToolCalls)
	}}
	deps.Projection.UpdateMessage = func(ctx context.Context, sessionID, messageID string, msg api.Message) error {
		updates++
		_, err := st.UpdateMessage(ctx, sessionID, messageID, msg)
		return err
	}
	deps.Model.LLM = tokenStreamingLLM{tokens: []string{"Hel", "lo", " ", "world"}}
	deps.Context.Policy = &recordingToolPolicy{}
	loop := promptloop.NewPromptLoopForTest(deps)
	ctx := context.Background()
	sess, err := st.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	result, err := loop.Run(ctx, promptloop.PromptRunInput{
		SessionID: sess.ID,
		Session:   sess,
		History:   userHistory("go"),
		ProfileID: "coordinator",
	})
	testutil.FailErr(t, "loop.Run", err)
	if result.LastAssistantContent != "Hello world" {
		t.Fatalf("content = %q want Hello world", result.LastAssistantContent)
	}
	// [OAR-OPS-8] No unreviewed chunk crosses the live projection boundary.
	if live != 0 {
		t.Fatalf("unreviewed content was projected %d times", live)
	}
	if updates == 0 {
		t.Fatal("UpdateMessage was never called on settle")
	}

}

func TestStreamProgressBuffersAllToolCalls(t *testing.T) {
	st := store.NewMemory()
	deps := promptloop.StoreDeps(st)
	var sawCommand bool
	deps.Projection.Streams = &observedMessageStreams{MessageStreams: deps.Projection.Streams, project: func(ctx context.Context, sessionID string, msg api.Message) error {
		for _, call := range msg.ToolCalls {
			if call.Name == "summarize" && strings.TrimSpace(call.ID) != "" {
				sawCommand = true
			}
			if call.Name == "read" {
				t.Fatal("live projection must not carry deferred read tool_calls")
			}
		}
		return st.PatchLiveProjection(ctx, sessionID, msg.ID, msg.Content, msg.ToolCalls)
	}}
	deps.Model.LLM = &toolStreamingLLM{calls: []api.ToolCall{
		{ID: "tc-sum", Name: "summarize", Args: map[string]any{"path": "pkg"}},
		{ID: "tc-read", Name: "read", Args: map[string]any{"path": "a.go"}},
	}}
	reg := tools.NewStubRegistry()
	_ = reg.Register("summarize", func(_ context.Context, _ map[string]any, _ tools.ToolContext) (string, error) {
		return "ok", nil
	})
	_ = reg.Register("read", func(_ context.Context, _ map[string]any, _ tools.ToolContext) (string, error) {
		return "file", nil
	})
	deps.Context.Tools = reg
	deps.Context.Policy = &recordingToolPolicy{}
	deps.Context.Limits = loopTestLimits(2)
	loop := promptloop.NewPromptLoopForTest(deps)
	ctx := context.Background()
	sess, err := st.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	_, err = loop.Run(ctx, promptloop.PromptRunInput{
		SessionID: sess.ID,
		Session:   sess,
		History:   userHistory("run"),
		ProfileID: "coordinator",
	})
	testutil.FailErr(t, "loop.Run", err)
	if sawCommand {
		t.Fatal("[OAR-OPS-8] incomplete tool call escaped the policy buffer")
	}
}

func TestLoopPublishesWorkerProgress(t *testing.T) {
	store := store.NewMemory()
	var published []publishedProgress
	var lastCtx *api.WorkerContextUsage
	deps := promptloop.StoreDeps(store)
	deps.Model.LLM = usageReportingLLM{usage: modelcall.TokenUsage{PromptTokens: 1234, CompletionTokens: 10}}
	deps.Context.Policy = &recordingToolPolicy{}
	deps.Model.CompactionConfig = staticCompactionConfig
	deps.Nudges.PublishWorkerProgress = func(_ context.Context, _ string, snap workerprogress.Snapshot, checkpoint bool) {
		published = append(published, publishedProgress{snap: snap, checkpoint: checkpoint})
		if snap.ContextUsage != nil {
			lastCtx = snap.ContextUsage
		}
	}
	loop := promptloop.NewPromptLoopForTest(deps)
	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)
	sess.ParentSessionID = "parent-1"
	sess.MaxToolLoops = 5
	_, err = loop.Run(ctx, promptloop.PromptRunInput{
		SessionID: sess.ID,
		Session:   sess,
		History:   userHistory("go"),
		ProfileID: "explore_readonly",
		ToolCtx: tools.ToolContext{
			Identity: tools.InvocationIdentity{SessionID: sess.ID,
				WorkerJobID: "job-1"},
		},
	})
	testutil.FailErr(t, "loop.Run failed", err)
	if len(published) == 0 {
		t.Fatal("expected worker progress publishes")
	}
	if published[0].snap.ToolLoopsUsed != 1 {
		t.Fatalf("first progress used = %d want 1; got %+v", published[0].snap.ToolLoopsUsed, published)
	}
	foundTerminal := false
	var maxUsed int
	for _, row := range published {
		if row.snap.ToolLoopsUsed > maxUsed {
			maxUsed = row.snap.ToolLoopsUsed
		}
		if row.checkpoint {
			foundTerminal = true
			if row.snap.ToolLoopsUsed < maxUsed {
				t.Fatalf("checkpoint used = %d below live max %d; got %+v", row.snap.ToolLoopsUsed, maxUsed, published)
			}
		}
	}
	if !foundTerminal {
		t.Fatalf("expected a checkpointing publish, got %+v", published)
	}
	if lastCtx == nil {
		t.Fatal("expected worker context usage on a published progress event")
	}
	if lastCtx.PromptTokens != 1234 {
		t.Fatalf("worker context prompt tokens = %d want 1234", lastCtx.PromptTokens)
	}
	if lastCtx.Window != compaction.DefaultCompactionConfig().ModelContextWindow {
		t.Fatalf("worker context window = %d want %d", lastCtx.Window, compaction.DefaultCompactionConfig().ModelContextWindow)
	}
}

// Settled calls publish progress within a round.
func TestLoopPublishesAnEdgePerSettledToolCall(t *testing.T) {
	client := &sequentialLLMClient{completions: []*modelcall.Completion{
		{ToolCalls: []api.ToolCall{
			{ID: "tc1", Name: "read", Args: map[string]any{"path": "a"}},
			{ID: "tc2", Name: "read", Args: map[string]any{"path": "b"}},
			{ID: "tc3", Name: "read", Args: map[string]any{"path": "c"}},
		}},
		{Content: "done"},
	}}
	store := store.NewMemory()
	var published []publishedProgress
	deps := promptloop.StoreDeps(store)
	deps.Model.LLM = client
	deps.Context.Tools = tools.NewStubRegistry()
	deps.Context.Policy = &recordingToolPolicy{}
	deps.Nudges.PublishWorkerProgress = func(_ context.Context, _ string, snap workerprogress.Snapshot, checkpoint bool) {
		published = append(published, publishedProgress{snap: snap, checkpoint: checkpoint})
	}
	loop := promptloop.NewPromptLoopForTest(deps)
	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)
	sess.AgentType = orchestration.ProfilePathExplorer
	sess.ParentSessionID = "parent-1"
	sess.MaxToolLoops = 5
	_, err = loop.Run(ctx, promptloop.PromptRunInput{
		SessionID: sess.ID,
		Session:   sess,
		History:   userHistory("go"),
		ProfileID: "explore_readonly",
		ToolCtx: tools.ToolContext{
			Identity: tools.InvocationIdentity{SessionID: sess.ID,
				WorkerJobID: "job-1"},
		},
	})
	testutil.FailErr(t, "loop.Run failed", err)

	var batchFractions [][2]int
	var settled int
	for _, row := range published {
		if row.snap.TurnToolCalls > 0 {
			batchFractions = append(batchFractions, [2]int{row.snap.TurnToolsDone, row.snap.TurnToolCalls})
		}
		if row.snap.ToolCallsUsed > settled {
			settled = row.snap.ToolCallsUsed
		}
	}
	want := [][2]int{{0, 3}, {1, 3}, {2, 3}, {3, 3}}
	if len(batchFractions) != len(want) {
		t.Fatalf("batch edges = %v want %v", batchFractions, want)
	}
	for i, got := range batchFractions {
		if got != want[i] {
			t.Fatalf("batch edges = %v want %v", batchFractions, want)
		}
	}
	if settled != 3 {
		t.Fatalf("lifetime tool calls = %d want 3", settled)
	}
	last := published[len(published)-1]
	if last.snap.TurnToolCalls != 0 || last.snap.TurnToolsDone != 0 {
		t.Fatalf("final edge = %+v want no in-flight batch", last.snap)
	}
	// Tool-call edges are SSE only; rows are written at round boundaries.
	for _, row := range published {
		if row.checkpoint && row.snap.TurnToolCalls > 0 {
			t.Fatalf("checkpointed mid-batch: %+v", row)
		}
	}
}
