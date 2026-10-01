package openaicompat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerretry"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestOpenAIComplete(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("authorization = %q", got)
		}
		resp := map[string]any{
			"choices": []map[string]any{
				{
					"message": map[string]any{
						"role":    "assistant",
						"content": "Hello!",
					},
				},
			},
			"usage": map[string]any{
				"prompt_tokens":     10,
				"completion_tokens": 5,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer mockServer.Close()

	provider := New("test", mockServer.URL, "test-key", []modelinfo.Entry{{ID: "gpt-4o"}})
	completion, err := provider.Complete(context.Background(), modelcall.CompletionRequest{
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "Hello"}},
	})
	testutil.FailErr(t, "provider.Complete failed", err)
	if completion.Content != "Hello!" {
		t.Fatalf("content = %q", completion.Content)
	}
	if completion.Usage.PromptTokens != 10 || completion.Usage.CompletionTokens != 5 {
		t.Fatalf("usage = %+v", completion.Usage)
	}
}

func TestOpenAICompatibleKeylessRequestOmitsAuthorization(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if authorization := r.Header.Get("Authorization"); authorization != "" {
			t.Fatalf("keyless request authorization = %q", authorization)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer server.Close()

	provider := New("local", server.URL, "", []modelinfo.Entry{{ID: "model"}})
	_, err := provider.Complete(t.Context(), modelcall.CompletionRequest{
		Model:    "model",
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hello"}},
	})
	testutil.FailErr(t, "keyless completion", err)
}

func TestOpenAIStream(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected flusher")
		}
		chunks := []string{"Hello", " ", "world", "!"}
		for _, chunk := range chunks {
			data, _ := json.Marshal(map[string]any{
				"choices": []map[string]any{
					{"delta": map[string]any{"content": chunk}},
				},
			})
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
		fmt.Fprintln(w, "data: [DONE]")
		flusher.Flush()
	}))
	defer mockServer.Close()

	provider := New("test", mockServer.URL, "test-key", []modelinfo.Entry{{ID: "gpt-4o"}})
	ch, err := provider.Stream(context.Background(), modelcall.CompletionRequest{
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "Hello"}},
	})
	testutil.FailErr(t, "provider.Stream failed", err)

	var tokens []string
	for chunk := range ch {
		if chunk.Done {
			break
		}
		if chunk.Content != "" {
			tokens = append(tokens, chunk.Content)
		}
	}
	want := []string{"Hello", " ", "world", "!"}
	if len(tokens) != len(want) {
		t.Fatalf("tokens = %v, want %v", tokens, want)
	}
	for i := range want {
		if tokens[i] != want[i] {
			t.Fatalf("token[%d] = %q, want %q", i, tokens[i], want[i])
		}
	}
}

func TestOpenAIStreamReasoningFieldsCaptured(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		emit := func(delta map[string]any) {
			data, _ := json.Marshal(map[string]any{"choices": []map[string]any{{"delta": delta}}})
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
		emit(map[string]any{"reasoning": "weigh "})
		emit(map[string]any{"reasoning_content": "options"})
		emit(map[string]any{"content": "answer"})
		fmt.Fprintln(w, "data: [DONE]")
		flusher.Flush()
	}))
	defer mockServer.Close()

	provider := New("test", mockServer.URL, "test-key", []modelinfo.Entry{{ID: "deepseek-r1"}})
	ch, err := provider.Stream(context.Background(), modelcall.CompletionRequest{
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "decide"}},
	})
	testutil.FailErr(t, "provider.Stream failed", err)
	completion, _, err := modelcall.CollectStream(ch)
	testutil.FailErr(t, "collect", err)
	if completion.Content != "answer" {
		t.Fatalf("content = %q, reasoning leaked into content?", completion.Content)
	}
	if completion.Reasoning != "weigh options" {
		t.Fatalf("reasoning = %q, want %q", completion.Reasoning, "weigh options")
	}
}

func TestMessagesToWireToolLoop(t *testing.T) {
	msgs := []api.Message{
		{Role: api.MessageRoleUser, Content: "read file"},
		{
			Role:      api.MessageRoleAssistant,
			ToolCalls: []api.ToolCall{{ID: "call_1", Name: "read", Args: map[string]any{"path": "a.txt"}}},
		},
		{Role: api.MessageRoleTool, Content: "file contents", ToolResult: &api.ToolResult{ToolCallID: "call_1", Content: "file contents"}},
		{Role: api.MessageRoleUser, Content: "thanks"},
	}
	converted := ProjectMessages(msgs, MessageProjection{})
	if len(converted) != 4 {
		t.Fatalf("len = %d", len(converted))
	}
	if len(converted[1].ToolCalls) != 1 || converted[1].ToolCalls[0].ID != "call_1" {
		t.Fatalf("assistant tool_calls = %+v", converted[1].ToolCalls)
	}
	if converted[1].ToolCalls[0].Function.Name != "read" {
		t.Fatalf("tool name = %q", converted[1].ToolCalls[0].Function.Name)
	}
	if converted[2].ToolCallID != "call_1" || converted[2].Content != "file contents" {
		t.Fatalf("tool message = %+v", converted[2])
	}
}

func TestMessagesToWireDropsOrphanTool(t *testing.T) {
	msgs := []api.Message{
		{Role: api.MessageRoleUser, Content: "promote"},
		{Role: api.MessageRoleTool, Content: "[host:overlay-promote-event] {\"merge_status\":\"merged\"}"},
	}
	converted := ProjectMessages(msgs, MessageProjection{})
	if len(converted) != 1 {
		t.Fatalf("len = %d", len(converted))
	}
	if converted[0].Role != "user" || converted[0].Content != "promote" {
		t.Fatalf("user row changed = %+v", converted[0])
	}
}

func TestMessagesToWireMultiTool(t *testing.T) {
	msgs := []api.Message{
		{Role: api.MessageRoleUser, Content: "go"},
		{
			Role: api.MessageRoleAssistant,
			ToolCalls: []api.ToolCall{
				{ID: "call_a", Name: "read", Args: map[string]any{"path": "a"}},
				{ID: "call_b", Name: "command", Args: map[string]any{"command": "ls"}},
			},
		},
		{Role: api.MessageRoleTool, Content: "a-body", ToolResult: &api.ToolResult{ToolCallID: "call_a"}},
		{Role: api.MessageRoleTool, Content: "ls-out", ToolResult: &api.ToolResult{ToolCallID: "call_b"}},
	}
	converted := ProjectMessages(msgs, MessageProjection{})
	if converted[2].ToolCallID != "call_a" || converted[3].ToolCallID != "call_b" {
		t.Fatalf("tool_call_ids = %q %q", converted[2].ToolCallID, converted[3].ToolCallID)
	}
}

func TestMessagesToWireExtraContentRoundTrip(t *testing.T) {
	extra := map[string]any{
		"google": map[string]any{"thought_signature": "sig-abc"},
	}
	msgs := []api.Message{
		{Role: api.MessageRoleUser, Content: "go"},
		{
			Role: api.MessageRoleAssistant,
			ToolCalls: []api.ToolCall{{
				ID:           "call_1",
				Name:         "find",
				Args:         map[string]any{"name_glob": "*"},
				ExtraContent: extra,
			}},
		},
		{Role: api.MessageRoleTool, Content: "[]", ToolResult: &api.ToolResult{ToolCallID: "call_1"}},
	}
	wire := ProjectMessages(msgs, MessageProjection{})
	if len(wire[1].ToolCalls) != 1 {
		t.Fatalf("tool_calls = %+v", wire[1].ToolCalls)
	}
	got := wire[1].ToolCalls[0].ExtraContent
	if got == nil || got["google"] == nil {
		t.Fatalf("extra_content = %+v", got)
	}
}

func TestConvertMessagesToolLoopHTTPBody(t *testing.T) {
	var body struct {
		Messages []struct {
			Role      string `json:"role"`
			ToolCalls []struct {
				ID           string         `json:"id"`
				ExtraContent map[string]any `json:"extra_content"`
				Function     struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls,omitempty"`
			ToolCallID string `json:"tool_call_id,omitempty"`
			Content    string `json:"content,omitempty"`
		} `json:"messages"`
	}
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		resp := map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": "ok"}}},
			"usage":   map[string]any{"prompt_tokens": 1, "completion_tokens": 1},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer mockServer.Close()

	provider := New("test", mockServer.URL, "test-key", []modelinfo.Entry{{ID: "gpt-4o"}})
	_, err := provider.Complete(context.Background(), modelcall.CompletionRequest{
		Messages: []api.Message{
			{Role: api.MessageRoleUser, Content: "go"},
			{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{
				ID:   "call_1",
				Name: "read",
				Args: map[string]any{"path": "x"},
				ExtraContent: map[string]any{
					"google": map[string]any{"thought_signature": "sig-xyz"},
				},
			}}},
			{Role: api.MessageRoleTool, Content: "result", ToolResult: &api.ToolResult{ToolCallID: "call_1"}},
			{Role: api.MessageRoleUser, Content: "next"},
		},
	})
	testutil.FailErr(t, "provider.Complete failed", err)
	if len(body.Messages) < 3 {
		t.Fatalf("messages = %+v", body.Messages)
	}
	if len(body.Messages[1].ToolCalls) == 0 || body.Messages[1].ToolCalls[0].ID != "call_1" {
		t.Fatalf("assistant wire = %+v", body.Messages[1])
	}
	if body.Messages[1].ToolCalls[0].ExtraContent == nil {
		t.Fatalf("extra_content missing from wire body")
	}
	if body.Messages[2].ToolCallID != "call_1" {
		t.Fatalf("tool wire = %+v", body.Messages[2])
	}
}

func TestOpenAICompleteToolCalls(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"choices": []map[string]any{
				{
					"message": map[string]any{
						"role": "assistant",
						"tool_calls": []map[string]any{
							{
								"id":   "call_abc",
								"type": "function",
								"function": map[string]any{
									"name":      "read",
									"arguments": `{"path":"README.md"}`,
								},
								"extra_content": map[string]any{
									"google": map[string]any{"thought_signature": "sig-capture"},
								},
							},
						},
					},
				},
			},
			"usage": map[string]any{"prompt_tokens": 4, "completion_tokens": 6},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer mockServer.Close()

	provider := New("test", mockServer.URL, "test-key", []modelinfo.Entry{{ID: "gpt-4o"}})
	completion, err := provider.Complete(context.Background(), modelcall.CompletionRequest{
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "read"}},
	})
	testutil.FailErr(t, "provider.Complete failed", err)
	if len(completion.ToolCalls) != 1 {
		t.Fatalf("tool_calls = %+v", completion.ToolCalls)
	}
	// Identity is host-minted; the provider's emitted token is retained as WireID.
	if completion.ToolCalls[0].Name != "read" || completion.ToolCalls[0].WireID != "call_abc" {
		t.Fatalf("tool call = %+v", completion.ToolCalls[0])
	}
	if completion.ToolCalls[0].ID == "" || completion.ToolCalls[0].ID == "call_abc" {
		t.Fatalf("expected host-minted id distinct from provider token, got %q", completion.ToolCalls[0].ID)
	}
	if completion.ToolCalls[0].Args["path"] != "README.md" {
		t.Fatalf("args = %+v", completion.ToolCalls[0].Args)
	}
	if completion.ToolCalls[0].ExtraContent == nil {
		t.Fatalf("extra_content = %+v", completion.ToolCalls[0].ExtraContent)
	}
}

func TestOpenAIStreamToolCallsAccumulated(t *testing.T) {
	idx0 := 0
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected flusher")
		}
		chunks := []map[string]any{
			{"choices": []map[string]any{{"delta": map[string]any{
				"tool_calls": []map[string]any{{
					"index":    idx0,
					"id":       "call_stream",
					"type":     "function",
					"function": map[string]any{"name": "read"},
					"extra_content": map[string]any{
						"google": map[string]any{"thought_signature": "sig-stream"},
					},
				}},
			}}}},
			{"choices": []map[string]any{{"delta": map[string]any{
				"tool_calls": []map[string]any{{
					"index":    idx0,
					"function": map[string]any{"arguments": `{"path":`},
				}},
			}}}},
			{"choices": []map[string]any{{"delta": map[string]any{
				"tool_calls": []map[string]any{{
					"index":    idx0,
					"function": map[string]any{"arguments": `"x.txt"}`},
				}},
			}}}},
			{"choices": []map[string]any{{"finish_reason": "tool_calls"}}},
		}
		for _, chunk := range chunks {
			data, _ := json.Marshal(chunk)
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
		fmt.Fprintln(w, "data: [DONE]")
		flusher.Flush()
	}))
	defer mockServer.Close()

	provider := New("test", mockServer.URL, "test-key", []modelinfo.Entry{{ID: "gpt-4o"}})
	ch, err := provider.Stream(context.Background(), modelcall.CompletionRequest{
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "read"}},
	})
	testutil.FailErr(t, "provider.Stream failed", err)
	completion, _, err := modelcall.CollectStream(ch)
	testutil.FailErr(t, "CollectStream failed", err)
	if len(completion.ToolCalls) != 1 {
		t.Fatalf("tool_calls = %+v", completion.ToolCalls)
	}
	tc := completion.ToolCalls[0]
	// Host and wire identities remain distinct across streamed deltas.
	if tc.WireID != "call_stream" || tc.Name != "read" {
		t.Fatalf("tool call = %+v", tc)
	}
	if tc.ID == "" || tc.ID == "call_stream" {
		t.Fatalf("expected host-minted id distinct from provider token, got %q", tc.ID)
	}
	if tc.Args["path"] != "x.txt" {
		t.Fatalf("args = %+v", tc.Args)
	}
	if tc.ExtraContent == nil {
		t.Fatalf("extra_content = %+v", tc.ExtraContent)
	}
}

func TestOpenAIStreamUsageFinalChunk(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected flusher")
		}
		contentChunk, _ := json.Marshal(map[string]any{
			"choices": []map[string]any{{"delta": map[string]any{"content": "Hi"}}},
		})
		fmt.Fprintf(w, "data: %s\n\n", contentChunk)
		flusher.Flush()
		usageChunk, _ := json.Marshal(map[string]any{
			"choices": []map[string]any{{"delta": map[string]any{}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 11, "completion_tokens": 7},
		})
		fmt.Fprintf(w, "data: %s\n\n", usageChunk)
		flusher.Flush()
		fmt.Fprintln(w, "data: [DONE]")
		flusher.Flush()
	}))
	defer mockServer.Close()

	provider := New("test", mockServer.URL, "test-key", []modelinfo.Entry{{ID: "gpt-4o"}})
	ch, err := provider.Stream(context.Background(), modelcall.CompletionRequest{
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "Hello"}},
	})
	testutil.FailErr(t, "provider.Stream failed", err)
	completion, tokens, err := modelcall.CollectStream(ch)
	testutil.FailErr(t, "CollectStream failed", err)
	if completion.Content != "Hi" {
		t.Fatalf("content = %q", completion.Content)
	}
	if completion.Usage.PromptTokens != 11 || completion.Usage.CompletionTokens != 7 {
		t.Fatalf("usage = %+v", completion.Usage)
	}
	if len(tokens) != 1 || tokens[0] != "Hi" {
		t.Fatalf("tokens = %v", tokens)
	}
}

// Usage may arrive after the terminal content chunk.
func TestOpenAIStreamUsageTrailingChunk(t *testing.T) {
	var gotStreamOptions bool
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			StreamOptions *struct {
				IncludeUsage bool `json:"include_usage"`
			} `json:"stream_options"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err == nil {
			gotStreamOptions = body.StreamOptions != nil && body.StreamOptions.IncludeUsage
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected flusher")
		}
		for _, payload := range []map[string]any{
			{"choices": []map[string]any{{"delta": map[string]any{"content": "Hi"}}}},
			{"choices": []map[string]any{{"delta": map[string]any{}, "finish_reason": "stop"}}},
			{"choices": []map[string]any{}, "usage": map[string]any{"prompt_tokens": 21, "completion_tokens": 9}},
		} {
			data, _ := json.Marshal(payload)
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
		fmt.Fprintln(w, "data: [DONE]")
		flusher.Flush()
	}))
	defer mockServer.Close()

	provider := New("test", mockServer.URL, "test-key", []modelinfo.Entry{{ID: "gpt-4o"}})
	ch, err := provider.Stream(context.Background(), modelcall.CompletionRequest{
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "Hello"}},
	})
	testutil.FailErr(t, "provider.Stream failed", err)
	completion, _, err := modelcall.CollectStream(ch)
	testutil.FailErr(t, "CollectStream failed", err)
	if !gotStreamOptions {
		t.Fatal("expected stream_options.include_usage in request body")
	}
	if completion.Content != "Hi" {
		t.Fatalf("content = %q", completion.Content)
	}
	if completion.Usage.PromptTokens != 21 || completion.Usage.CompletionTokens != 9 {
		t.Fatalf("usage = %+v", completion.Usage)
	}
}

func TestOpenAIStreamToolCallUsageTrailingChunk(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected flusher")
		}
		for _, payload := range []map[string]any{
			{"choices": []map[string]any{{"delta": map[string]any{"tool_calls": []map[string]any{{
				"index": 0, "id": "call_1", "type": "function",
				"function": map[string]any{"name": "read", "arguments": `{"path":"a.go"}`},
			}}}}}},
			{"choices": []map[string]any{{"delta": map[string]any{}, "finish_reason": "tool_calls"}}},
			{"choices": []map[string]any{}, "usage": map[string]any{"prompt_tokens": 33, "completion_tokens": 4}},
		} {
			data, _ := json.Marshal(payload)
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
		fmt.Fprintln(w, "data: [DONE]")
		flusher.Flush()
	}))
	defer mockServer.Close()

	provider := New("test", mockServer.URL, "test-key", []modelinfo.Entry{{ID: "gpt-4o"}})
	ch, err := provider.Stream(context.Background(), modelcall.CompletionRequest{
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "Hello"}},
	})
	testutil.FailErr(t, "provider.Stream failed", err)
	completion, _, err := modelcall.CollectStream(ch)
	testutil.FailErr(t, "CollectStream failed", err)
	if len(completion.ToolCalls) != 1 || completion.ToolCalls[0].Name != "read" {
		t.Fatalf("tool calls = %+v", completion.ToolCalls)
	}
	if completion.Usage.PromptTokens != 33 || completion.Usage.CompletionTokens != 4 {
		t.Fatalf("usage = %+v", completion.Usage)
	}
}

func TestOpenAICompleteError(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid key"}`))
	}))
	defer mockServer.Close()

	provider := New("test", mockServer.URL, "bad-key", []modelinfo.Entry{{ID: "gpt-4o"}})
	_, err := provider.Complete(context.Background(), modelcall.CompletionRequest{
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "Hello"}},
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestOpenAICompleteGeminiArrayError(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`[{"error":{"code":403,"message":"blocked","details":[{"reason":"API_KEY_SERVICE_BLOCKED"}]}}]`))
	}))
	defer mockServer.Close()

	provider := New("gemini", mockServer.URL, "AQ.test", []modelinfo.Entry{{ID: "gemini-3.5-flash"}})
	_, err := provider.Complete(context.Background(), modelcall.CompletionRequest{
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "Hello"}},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	rejected, ok := providerretry.AsProviderRequestRejected(err)
	var response *providerretry.ProviderHTTPError
	if !ok || rejected.Status != http.StatusForbidden || !errors.As(err, &response) ||
		response.Code != "403" || response.Message != "blocked (API_KEY_SERVICE_BLOCKED)" {
		t.Fatalf("provider rejection lost response evidence: %v", err)
	}
}

func TestConvertOpenAIToolsDoesNotSanitizeFunctionRoot(t *testing.T) {
	out := ProjectTools([]tools.ToolMeta{{
		Name: "code_rewrite",
		ArgsSchema: map[string]any{
			"type":  "object",
			"anyOf": []any{map[string]any{"required": []any{"path"}}},
		},
	}})
	if len(out) != 1 || out[0].Function == nil {
		t.Fatalf("tools = %+v", out)
	}
	params, ok := out[0].Function.Parameters.(map[string]any)
	if !ok {
		t.Fatalf("parameters = %T", out[0].Function.Parameters)
	}
	if _, has := params["anyOf"]; !has {
		t.Fatal("convertOpenAITools must not strip root anyOf; TrimCoordinatorToolMeta is the gate")
	}
}

func TestEncodeOpenAIToolsUsesTrimmedFunctionRoots(t *testing.T) {
	untrimmed := tools.ToolMeta{
		Name: "code_rewrite",
		ArgsSchema: map[string]any{
			"type":     "object",
			"required": []any{"pattern", "rewrite"},
			"anyOf": []any{
				map[string]any{"required": []any{"path"}},
				map[string]any{"required": []any{"paths"}},
			},
			"properties": map[string]any{
				"path":    map[string]any{"type": "string"},
				"pattern": map[string]any{"type": "string"},
				"rewrite": map[string]any{"type": "string"},
			},
		},
	}
	provider := New("openai", "https://api.openai.com/v1", "key", []modelinfo.Entry{
		{ID: "gpt-5.6-terra"},
	})
	body, err := encodeChatCompletionRequest(modelcall.CompletionRequest{
		Model:    "gpt-5.6-terra",
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hi"}},
		Tools:    []tools.ToolMeta{tools.TrimCoordinatorToolMeta(untrimmed)},
	}, provider, false, controlOpts{})
	testutil.FailErr(t, "encode OpenAI tools", err)
	params := openAIWireFunctionParameters(t, body, "code_rewrite")
	if err := tools.ValidateFunctionParametersRoot(params); err != nil {
		t.Fatalf("wire function root: %v\n%s", err, body)
	}
}

func openAIWireFunctionParameters(t *testing.T, body []byte, name string) map[string]any {
	t.Helper()
	var wire map[string]any
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	listed, ok := wire["tools"].([]any)
	if !ok {
		t.Fatalf("tools missing: %s", body)
	}
	for _, raw := range listed {
		tool, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		fn, ok := tool["function"].(map[string]any)
		if !ok || fn["name"] != name {
			continue
		}
		params, ok := fn["parameters"].(map[string]any)
		if !ok {
			t.Fatalf("function %s parameters = %T", name, fn["parameters"])
		}
		return params
	}
	t.Fatalf("function %s missing: %s", name, body)
	return nil
}

func TestConvertOpenAIToolsParametersNeverNull(t *testing.T) {
	toolsOut := ProjectTools([]tools.ToolMeta{
		{Name: "no_schema", Description: "fallback"},
		{
			Name:        "with_schema",
			Description: "explicit",
			ArgsSchema:  map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}},
		},
	})
	raw, err := json.Marshal(toolsOut)
	testutil.FailErr(t, "marshal OpenAI tools", err)
	if strings.Contains(string(raw), `"parameters":null`) {
		t.Fatalf("OpenAI tools payload must not contain parameters=null:\n%s", string(raw))
	}
	for i, tool := range toolsOut {
		if tool.Function == nil || tool.Function.Parameters == nil {
			t.Fatalf("tools[%d].function.parameters is nil", i)
		}
	}
}

func TestResolveRequestControlsAppliesModelEntryControls(t *testing.T) {
	temp := 0.6
	provider := New("test", "http://localhost", "key", []modelinfo.Entry{
		{ID: "kimi-k2p6", Temperature: &temp, MaxTokens: 16384, ReasoningEffort: "low"},
		{ID: "plain"},
	})
	resolve := func(model string) requestControls {
		return provider.resolveRequestControls(modelcall.CompletionRequest{Model: model}, model, controlOpts{})
	}

	withControls := resolve("kimi-k2p6")
	if withControls.Temperature == nil || *withControls.Temperature != 0.6 {
		t.Fatalf("temperature = %v", withControls.Temperature)
	}
	if withControls.MaxTokens != 16384 {
		t.Fatalf("open turn max_tokens = %d", withControls.MaxTokens)
	}
	if withControls.ReasoningEffort != "low" {
		t.Fatalf("reasoning_effort = %q", withControls.ReasoningEffort)
	}

	if tagged := resolve("kimi-k2p6:latest"); tagged.ReasoningEffort != "medium" {
		t.Fatalf("OpenAI-compatible model ids must be opaque: reasoning_effort = %q", tagged.ReasoningEffort)
	}

	bare := resolve("plain")
	if bare.Temperature != nil || bare.MaxTokens != 0 || bare.ReasoningEffort != "medium" {
		t.Fatalf("plain model should use application controls, got %+v", bare)
	}

	unknown := resolve("not-in-catalog")
	if unknown.Temperature != nil || unknown.MaxTokens != 0 || unknown.ReasoningEffort != "medium" {
		t.Fatalf("unknown open turn should use application controls, got %+v", unknown)
	}
}
