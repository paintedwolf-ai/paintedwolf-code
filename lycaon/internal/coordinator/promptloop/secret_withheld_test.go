package promptloop_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providers/openaicompat"
	"github.com/lycaon/lycaon/internal/promptresult"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// withholdingLLMClient simulates declined secret approvals before replying.
type withholdingLLMClient struct {
	refusals int
	calls    int
	requests []modelcall.CompletionRequest
	reply    string
}

func (c *withholdingLLMClient) Complete(ctx context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	c.calls++
	c.requests = append(c.requests, req)
	if c.calls <= c.refusals {
		return nil, &llm.ModelRequestSecretWithheldError{Guidance: "use the staging key"}
	}
	return &modelcall.Completion{Content: c.reply}, nil
}

func (c *withholdingLLMClient) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	completion, err := c.Complete(ctx, req)
	if err != nil {
		return nil, err
	}
	ch := make(chan modelcall.StreamChunk, 1)
	ch <- modelcall.StreamChunk{Content: completion.Content, Done: true}
	close(ch)
	return ch, nil
}

func withheldNudgeDeps(st *store.Memory, client modelcall.LLMClient) promptloop.PromptLoopDeps {
	deps := promptloop.StoreDeps(st)
	deps.Model.LLM = client
	deps.Context.Policy = &recordingToolPolicy{}
	deps.Nudges.SecretWithheldNudge = func(_ context.Context, _ *api.Session, guidance string) promptloop.HostNudge {
		return promptloop.HostNudge{Content: "host: request not sent, user declined. guidance=" + guidance}
	}
	return deps
}

func TestSecretWithheldTurnTellsModelAndContinues(t *testing.T) {
	st := store.NewMemory()
	client := &withholdingLLMClient{refusals: 1, reply: "carried on without the key"}
	loop := promptloop.NewPromptLoopForTest(withheldNudgeDeps(st, client))
	ctx := context.Background()
	sess, err := st.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)

	result, err := loop.Run(ctx, promptloop.PromptRunInput{
		SessionID: sess.ID, Session: sess, History: userHistory("go"), ProfileID: "coordinator",
	})
	testutil.FailErr(t, "loop.Run must not fail on a declined credential", err)
	if result.LastAssistantContent != "carried on without the key" {
		t.Fatalf("content = %q want the turn taken after the refusal", result.LastAssistantContent)
	}
	if client.calls != 2 {
		t.Fatalf("provider calls = %d want 2 (refused, then retaken)", client.calls)
	}

	// The retry includes the refusal and user guidance.
	var sawNudge bool
	for _, msg := range client.requests[1].Messages {
		if strings.Contains(msg.Content, "user declined") && strings.Contains(msg.Content, "guidance=use the staging key") {
			sawNudge = true
		}
	}
	if !sawNudge {
		t.Fatalf("retry request carried no refusal notice: %+v", client.requests[1].Messages)
	}

	msgs, err := st.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "store.GetMessages failed", err)
	for _, msg := range msgs {
		if strings.Contains(msg.Content, "user declined") && msg.Visibility != api.MessageVisibilityInternal {
			t.Fatalf("refusal notice must stay internal, got visibility %q", msg.Visibility)
		}
	}
}

// Retained user text can trigger the same secret approval on each retry.
func TestSecretWithheldRepeatedRefusalEndsRunWithoutError(t *testing.T) {
	st := store.NewMemory()
	client := &withholdingLLMClient{refusals: 5, reply: "unreachable"}
	loop := promptloop.NewPromptLoopForTest(withheldNudgeDeps(st, client))
	ctx := context.Background()
	sess, err := st.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)

	result, err := loop.Run(ctx, promptloop.PromptRunInput{
		SessionID: sess.ID, Session: sess, History: userHistory("go"), ProfileID: "coordinator",
	})
	testutil.FailErr(t, "a repeated refusal must not fail the run", err)
	if result == nil {
		t.Fatal("result = nil want an empty run result")
	}
	if result.LastAssistantID != "" {
		t.Fatalf("last assistant = %q want none: no turn ever reached a provider", result.LastAssistantID)
	}
	if client.calls != 2 {
		t.Fatalf("provider calls = %d want 2 (one retry, then stop asking)", client.calls)
	}
}

type ceilingFixtureClient struct {
	*sequentialLLMClient
	entry     modelinfo.Entry
	maxTokens int
}

func (c *ceilingFixtureClient) ModelEntry(_ string) (modelinfo.Entry, bool) {
	return c.entry, c.entry.ID != ""
}

func (c *ceilingFixtureClient) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	if req.MaxTokens == 0 && c.maxTokens > 0 {
		req.MaxTokens = c.maxTokens
	}
	return c.sequentialLLMClient.Stream(ctx, req)
}

func TestLengthTruncatedToolCallRecoversOnceWithHostNotice(t *testing.T) {
	st := store.NewMemory()
	truncatedCall := api.ToolCall{
		ID:            "call_trunc_1",
		Name:          "write",
		ArgsTruncated: true,
	}
	truncatedCall.SetTruncatedArgsBytes(384)

	seq := &sequentialLLMClient{
		completions: []*modelcall.Completion{
			{
				ProviderID:   "openai",
				Model:        "gpt-4o",
				FinishReason: "length",
				ToolCalls:    []api.ToolCall{truncatedCall},
			},
			{
				ProviderID:   "openai",
				Model:        "gpt-4o",
				FinishReason: "stop",
				Content:      "Split the write and completed cleanly.",
			},
		},
	}
	client := &ceilingFixtureClient{
		sequentialLLMClient: seq,
		entry: modelinfo.Entry{
			ID:                "gpt-4o",
			MaxTokens:         4096,
			ProviderMaxTokens: 16384,
		},
		maxTokens: 4096,
	}

	var writeInvocations int
	reg := tools.NewStubRegistry()
	testutil.FailErr(t, "register write", reg.Register("write", func(_ context.Context, _ map[string]any, _ tools.ToolContext) (string, error) {
		writeInvocations++
		return "ok", nil
	}))

	deps := promptloop.StoreDeps(st)
	deps.Model.LLM = client
	deps.Context.Tools = reg
	deps.Context.Policy = &recordingToolPolicy{}
	loop := promptloop.NewPromptLoopForTest(deps)

	ctx := context.Background()
	sess, err := st.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	result, err := loop.Run(ctx, promptloop.PromptRunInput{
		SessionID: sess.ID,
		Session:   sess,
		History:   userHistory("write the module"),
		ProfileID: "coordinator",
	})
	testutil.FailErr(t, "loop.Run must recover on first length truncation", err)

	if writeInvocations != 0 {
		t.Fatalf("truncated tool call must be dropped, but write ran %d times", writeInvocations)
	}
	if len(seq.requests) != 2 {
		t.Fatalf("provider requests = %d, want 2 (initial + 1 recovery)", len(seq.requests))
	}
	if result.Outcome != promptresult.OutcomeCompleted {
		t.Fatalf("outcome = %q, want %q", result.Outcome, promptresult.OutcomeCompleted)
	}
	if result.LastAssistantContent != "Split the write and completed cleanly." {
		t.Fatalf("last assistant content = %q", result.LastAssistantContent)
	}

	// The second request carries the host recovery notice and host-written data.
	var recoveryMsg *api.Message
	for i := range seq.requests[1].Messages {
		m := &seq.requests[1].Messages[i]
		if m.Origin == api.MessageOriginHost && strings.Contains(m.Content, "truncated_tool: write") {
			recoveryMsg = m
			break
		}
	}
	if recoveryMsg == nil {
		t.Fatalf("second request missing host recovery notice: %+v", seq.requests[1].Messages)
	}
	if !strings.Contains(recoveryMsg.Content, "output_token_limit: 4096") {
		t.Fatalf("recovery notice missing output limit: %q", recoveryMsg.Content)
	}
	if !strings.Contains(recoveryMsg.Content, "truncated_tool_args_bytes: 384") {
		t.Fatalf("recovery notice missing truncated tool args bytes: %q", recoveryMsg.Content)
	}
	if !strings.Contains(recoveryMsg.Content, "configured_max_tokens: 4096 (provider allows 16384") {
		t.Fatalf("recovery notice missing low output ceiling diagnostic: %q", recoveryMsg.Content)
	}
	if len(recoveryMsg.ContentParts) < 2 {
		t.Fatalf("expected structured ContentParts on host recovery notice, got %+v", recoveryMsg.ContentParts)
	}
	dataPart := recoveryMsg.ContentParts[1]
	if dataPart.Origin != api.MessageOriginHost || dataPart.Authority != api.ContentAuthorityNone {
		t.Fatalf("expected host-written data part (Origin=host, Authority=none), got %+v", dataPart)
	}
}

func TestSecondLengthTruncationEndsTurnWithTypedOutcomeAndVisibleNotice(t *testing.T) {
	st := store.NewMemory()
	truncatedCall := api.ToolCall{
		ID:            "call_trunc_1",
		Name:          "write",
		ArgsTruncated: true,
	}
	truncatedCall.SetTruncatedArgsBytes(512)

	seq := &sequentialLLMClient{
		completions: []*modelcall.Completion{
			{
				ProviderID:   "anthropic",
				Model:        "claude-sonnet-4-5",
				FinishReason: "length",
				ToolCalls:    []api.ToolCall{truncatedCall},
			},
			{
				ProviderID:   "anthropic",
				Model:        "claude-sonnet-4-5",
				FinishReason: "length",
				ToolCalls:    []api.ToolCall{truncatedCall},
			},
		},
	}
	client := &ceilingFixtureClient{
		sequentialLLMClient: seq,
		entry: modelinfo.Entry{
			ID:                "claude-sonnet-4-5",
			MaxTokens:         8192,
			ProviderMaxTokens: 64000,
		},
		maxTokens: 8192,
	}

	deps := promptloop.StoreDeps(st)
	deps.Model.LLM = client
	deps.Context.Policy = &recordingToolPolicy{}
	loop := promptloop.NewPromptLoopForTest(deps)

	ctx := context.Background()
	sess, err := st.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	result, err := loop.Run(ctx, promptloop.PromptRunInput{
		SessionID: sess.ID,
		Session:   sess,
		History:   userHistory("write huge file"),
		ProfileID: "coordinator",
	})
	testutil.FailErr(t, "second length truncation must end turn cleanly without failing", err)
	if len(seq.requests) != 2 {
		t.Fatalf("provider requests = %d, want 2 (initial + 1 recovery)", len(seq.requests))
	}
	if result.Outcome != promptresult.OutcomeOutputTruncated {
		t.Fatalf("result.Outcome = %q, want %q", result.Outcome, promptresult.OutcomeOutputTruncated)
	}
	if result.OutputTruncated == nil {
		t.Fatal("expected OutputTruncated details on result")
	}
	if !result.OutputTruncated.ToolCallTruncated || result.OutputTruncated.ToolName != "write" || result.OutputTruncated.ToolArgsBytes != 512 {
		t.Fatalf("unexpected tool metadata on OutputTruncated: %+v", result.OutputTruncated)
	}
	if !result.OutputTruncated.LowOutputCeiling || result.OutputTruncated.ConfiguredMaxTokens != 8192 || result.OutputTruncated.ProviderMaxTokens != 64000 {
		t.Fatalf("unexpected ceiling metadata on OutputTruncated: %+v", result.OutputTruncated)
	}
	if !strings.Contains(result.LastAssistantContent, "write") ||
		!strings.Contains(result.LastAssistantContent, "8192") ||
		!strings.Contains(result.LastAssistantContent, "64000") {
		t.Fatalf("visible terminal notice missing expected details: %q", result.LastAssistantContent)
	}

	msgs, err := st.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "GetMessages", err)
	var sawVisibleNotice bool
	for _, msg := range msgs {
		if msg.ID == result.LastAssistantID {
			if msg.Visibility != api.MessageVisibilityTranscript {
				t.Fatalf("terminal notice visibility = %q, want transcript", msg.Visibility)
			}
			sawVisibleNotice = true
		}
	}
	if !sawVisibleNotice {
		t.Fatalf("terminal visible notice message %q not persisted in session messages", result.LastAssistantID)
	}
}

func TestOpenAICompatSSEStreamLengthTruncatedToolCallRecoversInPromptLoop(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt := calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected flusher")
		}
		if attempt == 1 {
			chunks := []map[string]any{
				{"choices": []map[string]any{{"delta": map[string]any{
					"tool_calls": []map[string]any{{
						"index": 0,
						"id":    "call_trunc_sse",
						"type":  "function",
						"function": map[string]any{
							"name":      "write",
							"arguments": `{"path":"big.go","content":"unterminated`,
						},
					}},
				}}}},
				{"choices": []map[string]any{{"finish_reason": "length"}}},
			}
			for _, c := range chunks {
				b, _ := json.Marshal(c)
				fmt.Fprintf(w, "data: %s\n\n", b)
				flusher.Flush()
			}
		} else {
			chunk, _ := json.Marshal(map[string]any{
				"choices": []map[string]any{{"delta": map[string]any{"content": "Recovered after splitting."}, "finish_reason": "stop"}},
			})
			fmt.Fprintf(w, "data: %s\n\n", chunk)
			flusher.Flush()
		}
		fmt.Fprintln(w, "data: [DONE]")
		flusher.Flush()
	}))
	defer srv.Close()

	provider := openaicompat.New("openai-test", srv.URL, "test-key", []modelinfo.Entry{{
		ID:                "gpt-4o",
		MaxTokens:         4096,
		ProviderMaxTokens: 16384,
	}})
	st := store.NewMemory()
	deps := promptloop.StoreDeps(st)
	deps.Model.LLM = provider
	deps.Context.Policy = &recordingToolPolicy{}
	loop := promptloop.NewPromptLoopForTest(deps)

	ctx := context.Background()
	sess, err := st.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	result, err := loop.Run(ctx, promptloop.PromptRunInput{
		SessionID: sess.ID,
		Session:   sess,
		History:   userHistory("write big.go"),
		ProfileID: "coordinator",
	})
	testutil.FailErr(t, "loop.Run failed", err)
	if calls.Load() != 2 {
		t.Fatalf("SSE calls = %d, want 2", calls.Load())
	}
	if result.LastAssistantContent != "Recovered after splitting." {
		t.Fatalf("content = %q", result.LastAssistantContent)
	}
}
