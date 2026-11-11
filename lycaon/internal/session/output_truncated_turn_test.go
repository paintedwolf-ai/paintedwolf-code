package session

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/promptresult"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testtool"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/oartest"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type outputTruncatedFixtureLLM struct {
	entry       modelinfo.Entry
	maxTokens   int
	completions []*modelcall.Completion
	requests    []modelcall.CompletionRequest
	idx         int
}

func (f *outputTruncatedFixtureLLM) ModelEntry(_ string) (modelinfo.Entry, bool) {
	return f.entry, f.entry.ID != ""
}

func (f *outputTruncatedFixtureLLM) Complete(_ context.Context, _ modelcall.CompletionRequest) (*modelcall.Completion, error) {
	if f.idx >= len(f.completions) {
		return &modelcall.Completion{Content: "unexpected extra call", FinishReason: "stop"}, nil
	}
	out := *f.completions[f.idx]
	f.idx++
	return &out, nil
}

func (f *outputTruncatedFixtureLLM) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	if req.MaxTokens == 0 && f.maxTokens > 0 {
		req.MaxTokens = f.maxTokens
	}
	f.requests = append(f.requests, req)
	completion, err := f.Complete(ctx, req)
	if err != nil {
		return nil, err
	}
	ch := make(chan modelcall.StreamChunk, 1)
	ch <- modelcall.StreamChunk{
		Content:      completion.Content,
		ToolCalls:    completion.ToolCalls,
		FinishReason: completion.FinishReason,
		ProviderID:   completion.ProviderID,
		Model:        completion.Model,
		Done:         true,
	}
	close(ch)
	return ch, nil
}

func newOutputTruncatedTestHost(t *testing.T, client modelcall.LLMClient) (*Host, *store.Memory) {
	t.Helper()
	memory := store.NewMemory()
	registry := tools.NewStubRegistry()
	_ = registry.Register("write", func(_ context.Context, _ map[string]any, _ tools.ToolContext) (string, error) {
		return "written", nil
	})
	mgr := NewHost(memory, Models{
		Client: client,
		Limits: settings.DefaultSessionLimits(),
	}, registry)
	oartest.InstallCloseoutPolicy(t, mgr)
	mgr.Coordinator.Guards.SetToolMetadata(testtool.RegistryInvoker{Registry: registry})
	mgr.SetDataDir(t.TempDir())
	return mgr, memory
}

func TestCoordinatorTurnRecoversOnceFromLengthTruncatedToolCall(t *testing.T) {
	tc := api.ToolCall{
		ID:            "call_write_trunc",
		Name:          "write",
		ArgsTruncated: true,
	}
	tc.SetTruncatedArgsBytes(256)

	client := &outputTruncatedFixtureLLM{
		entry: modelinfo.Entry{
			ID:                "gpt-4o",
			MaxTokens:         4096,
			ProviderMaxTokens: 16384,
		},
		maxTokens: 4096,
		completions: []*modelcall.Completion{
			{
				ProviderID:   "openai",
				Model:        "gpt-4o",
				FinishReason: "length",
				ToolCalls:    []api.ToolCall{tc},
			},
			{
				ProviderID:   "openai",
				Model:        "gpt-4o",
				FinishReason: "stop",
				Content:      "Wrote the file in smaller steps.",
			},
		},
	}

	mgr, memory := newOutputTruncatedTestHost(t, client)
	ctx := context.Background()
	sess, err := memory.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	resp, err := mgr.Submissions.Prompt(ctx, sess.ID, "write the large file")
	testutil.FailErr(t, "Prompt must recover on first length truncation", err)
	if resp.Outcome != promptresult.OutcomeCompleted {
		t.Fatalf("resp.Outcome = %q, want %q", resp.Outcome, promptresult.OutcomeCompleted)
	}
	if len(client.requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(client.requests))
	}

	status, err := memory.LatestTurnStatus(ctx, sess.ID)
	testutil.FailErr(t, "LatestTurnStatus", err)
	if status != store.TurnStatusComplete {
		t.Fatalf("turn status = %q, want %q", status, store.TurnStatusComplete)
	}
}

func TestCoordinatorTurnSecondLengthTruncationRecordsTypedOutcomeWithoutFailedTurn(t *testing.T) {
	tc := api.ToolCall{
		ID:            "call_write_trunc",
		Name:          "write",
		ArgsTruncated: true,
	}
	tc.SetTruncatedArgsBytes(512)

	client := &outputTruncatedFixtureLLM{
		entry: modelinfo.Entry{
			ID:                "claude-sonnet-4-5",
			MaxTokens:         8192,
			ProviderMaxTokens: 64000,
		},
		maxTokens: 8192,
		completions: []*modelcall.Completion{
			{
				ProviderID:   "anthropic",
				Model:        "claude-sonnet-4-5",
				FinishReason: "length",
				ToolCalls:    []api.ToolCall{tc},
			},
			{
				ProviderID:   "anthropic",
				Model:        "claude-sonnet-4-5",
				FinishReason: "length",
				ToolCalls:    []api.ToolCall{tc},
			},
		},
	}

	mgr, memory := newOutputTruncatedTestHost(t, client)
	ctx := context.Background()
	sess, err := memory.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	resp, err := mgr.Submissions.Prompt(ctx, sess.ID, "write the huge file")
	testutil.FailErr(t, "Prompt must not fail on second length truncation", err)
	if resp == nil {
		t.Fatal("expected non-nil prompt result")
	}
	if resp.Outcome != promptresult.OutcomeOutputTruncated {
		t.Fatalf("resp.Outcome = %q, want %q", resp.Outcome, promptresult.OutcomeOutputTruncated)
	}
	if resp.OutputTruncated == nil {
		t.Fatal("expected resp.OutputTruncated details")
	}
	if !resp.OutputTruncated.ToolCallTruncated || resp.OutputTruncated.ToolName != "write" || resp.OutputTruncated.ToolArgsBytes != 512 {
		t.Fatalf("unexpected tool metadata on resp.OutputTruncated: %+v", resp.OutputTruncated)
	}
	if !resp.OutputTruncated.LowOutputCeiling || resp.OutputTruncated.ConfiguredMaxTokens != 8192 || resp.OutputTruncated.ProviderMaxTokens != 64000 {
		t.Fatalf("unexpected ceiling metadata on resp.OutputTruncated: %+v", resp.OutputTruncated)
	}
	if !strings.Contains(resp.OutputTruncated.Notice, "8192") || !strings.Contains(resp.OutputTruncated.Notice, "64000") {
		t.Fatalf("expected low output ceiling guidance in notice, got %q", resp.OutputTruncated.Notice)
	}

	status, err := memory.LatestTurnStatus(ctx, sess.ID)
	testutil.FailErr(t, "LatestTurnStatus", err)
	if status != store.TurnStatusComplete {
		t.Fatalf("turn status = %q, want %q", status, store.TurnStatusComplete)
	}
}
