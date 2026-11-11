package openaicompat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/llm/providerwire"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestMistralToolCallIDNormalizationAndContinuation(t *testing.T) {
	msgs := []api.Message{
		{Role: api.MessageRoleUser, Content: "inspect files"},
		{
			Role: api.MessageRoleAssistant,
			ToolCalls: []api.ToolCall{
				// Already a valid 9-char alphanumeric Mistral wire ID: preserved verbatim.
				{ID: "call_host_1", WireID: "AbCdEf123", Name: "read", Args: map[string]any{"path": "a.go"}},
				// Host-minted or foreign wire ID: deterministically normalized to 9 alphanumeric chars.
				{ID: "call_host_2", WireID: "toolu_01A09q90qw90lq917835lq9", Name: "read", Args: map[string]any{"path": "b.go"}},
				// No wire ID: host ID normalized to 9 alphanumeric chars.
				{ID: "call_e4f5a6b7", Name: "read", Args: map[string]any{"path": "c.go"}},
			},
		},
		{Role: api.MessageRoleTool, Content: "package a", ToolResult: &api.ToolResult{ToolCallID: "call_host_1"}},
		{Role: api.MessageRoleTool, Content: "package b", ToolResult: &api.ToolResult{ToolCallID: "call_host_2"}},
		{Role: api.MessageRoleTool, Content: "package c", ToolResult: &api.ToolResult{ToolCallID: "call_e4f5a6b7"}},
	}

	proj := MessageProjection{
		ToolCallIDStyle: providerprofile.Mistral().ToolCallID,
	}
	wire1 := ProjectMessages(msgs, proj)
	wire2 := ProjectMessages(msgs, proj)

	if len(wire1) != 5 {
		t.Fatalf("projected messages = %d, want 5", len(wire1))
	}
	calls := wire1[1].ToolCalls
	if len(calls) != 3 {
		t.Fatalf("assistant tool_calls = %d, want 3", len(calls))
	}
	if calls[0].ID != "AbCdEf123" {
		t.Fatalf("valid 9-char WireID changed: got %q, want AbCdEf123", calls[0].ID)
	}
	seen := map[string]bool{}
	for idx, tc := range calls {
		if !providerwire.IsAlphanumeric9ToolCallID(tc.ID) {
			t.Fatalf("call[%d] ID %q is not 9-char alphanumeric", idx, tc.ID)
		}
		if seen[tc.ID] {
			t.Fatalf("duplicate normalized tool call ID %q", tc.ID)
		}
		seen[tc.ID] = true
		if wire2[1].ToolCalls[idx].ID != tc.ID {
			t.Fatalf("normalization not deterministic at call[%d]: %q vs %q", idx, tc.ID, wire2[1].ToolCalls[idx].ID)
		}
		toolMsg := wire1[2+idx]
		if toolMsg.ToolCallID != tc.ID {
			t.Fatalf("tool result[%d] tool_call_id = %q, want paired %q", idx, toolMsg.ToolCallID, tc.ID)
		}
	}
}

func TestMistralToolCallIDCollisionAvoidance(t *testing.T) {
	// Two tool calls in the same assistant wave with identical candidate WireIDs
	// receive distinct 9-character alphanumeric IDs and pair to their respective results.
	msgs := []api.Message{
		{
			Role: api.MessageRoleAssistant,
			ToolCalls: []api.ToolCall{
				{ID: "host_1", WireID: "SameWire9", Name: "read"},
				{ID: "host_2", WireID: "SameWire9", Name: "read"},
				{ID: "", Name: "read"},
			},
		},
		{Role: api.MessageRoleTool, Content: "one", ToolResult: &api.ToolResult{ToolCallID: "host_1"}},
		{Role: api.MessageRoleTool, Content: "two", ToolResult: &api.ToolResult{ToolCallID: "host_2"}},
	}
	wire := ProjectMessages(msgs, MessageProjection{
		ToolCallIDStyle: providerprofile.ToolCallIDAlphanumeric9,
	})
	if len(wire[0].ToolCalls) != 3 {
		t.Fatalf("tool_calls = %d, want 3", len(wire[0].ToolCalls))
	}
	id0 := wire[0].ToolCalls[0].ID
	id1 := wire[0].ToolCalls[1].ID
	id2 := wire[0].ToolCalls[2].ID
	for _, id := range []string{id0, id1, id2} {
		if !providerwire.IsAlphanumeric9ToolCallID(id) {
			t.Fatalf("id %q is not 9-char alphanumeric", id)
		}
	}
	if id0 == id1 || id0 == id2 || id1 == id2 {
		t.Fatalf("expected distinct IDs across wave, got %q, %q, %q", id0, id1, id2)
	}
	if wire[1].ToolCallID != id0 || wire[2].ToolCallID != id1 {
		t.Fatalf("tool results paired to %q and %q, want %q and %q", wire[1].ToolCallID, wire[2].ToolCallID, id0, id1)
	}
}

func TestMistralStreamingToolTurnContinuationAndUsage(t *testing.T) {
	turn := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer mistral-secret" {
			http.Error(w, `{"message":"Unauthorized"}`, http.StatusUnauthorized)
			return
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var reqBody struct {
			Model    string `json:"model"`
			Messages []struct {
				Role       string `json:"role"`
				Content    any    `json:"content"`
				ToolCallID string `json:"tool_call_id"`
				ToolCalls  []struct {
					ID string `json:"id"`
				} `json:"tool_calls"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(raw, &reqBody); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// Enforce Mistral's strict 9-char alphanumeric tool_call_id contract on every message.
		for _, m := range reqBody.Messages {
			for _, tc := range m.ToolCalls {
				if !providerwire.IsAlphanumeric9ToolCallID(tc.ID) {
					http.Error(w, fmt.Sprintf(`{"object":"error","message":"Tool call id was %s but must be a-z, A-Z, 0-9, with a length of 9."}`, tc.ID), http.StatusBadRequest)
					return
				}
			}
			if m.Role == "tool" && !providerwire.IsAlphanumeric9ToolCallID(m.ToolCallID) {
				http.Error(w, fmt.Sprintf(`{"object":"error","message":"Tool call id was %s but must be a-z, A-Z, 0-9, with a length of 9."}`, m.ToolCallID), http.StatusBadRequest)
				return
			}
		}

		w.Header().Set("Content-Type", "text/event-stream")
		turn++
		if turn == 1 {
			_, _ = fmt.Fprint(w, "data: {\"id\":\"cmpl-1\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"tool_calls\":[{\"index\":0,\"id\":\"MstCall01\",\"type\":\"function\",\"function\":{\"name\":\"read\",\"arguments\":\"{\\\"path\\\":\"}}]}}]}\n\n")
			_, _ = fmt.Fprint(w, "data: {\"id\":\"cmpl-1\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"\\\"README.md\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}],\"usage\":{\"prompt_tokens\":42,\"completion_tokens\":11,\"total_tokens\":53}}\n\n")
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		_, _ = fmt.Fprint(w, "data: {\"id\":\"cmpl-2\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"File read complete.\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":68,\"completion_tokens\":5,\"total_tokens\":73}}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	provider := New("mistral", srv.URL+"/v1", "mistral-secret", []modelinfo.Entry{
		{ID: "mistral-large-latest", ContextLength: 131072, MaxTokens: 32768},
	}).WithProfile(providerprofile.Mistral())

	// Turn 1: stream a tool call from Mistral.
	ch, err := provider.Stream(context.Background(), modelcall.CompletionRequest{
		Model:    "mistral-large-latest",
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "Read README.md"}},
		Tools:    []tools.ToolMeta{{Name: "read", Description: "Read file"}},
	})
	if err != nil {
		t.Fatalf("Turn 1 Stream: %v", err)
	}
	comp1, _, err := modelcall.CollectStream(ch)
	if err != nil {
		t.Fatalf("Turn 1 CollectStream: %v", err)
	}
	if len(comp1.ToolCalls) != 1 || comp1.ToolCalls[0].Name != "read" || comp1.ToolCalls[0].WireID != "MstCall01" {
		t.Fatalf("Turn 1 tool calls = %+v", comp1.ToolCalls)
	}
	if comp1.Usage.PromptTokens != 42 || comp1.Usage.CompletionTokens != 11 {
		t.Fatalf("Turn 1 usage = %+v", comp1.Usage)
	}

	// Turn 2: replay both a prior foreign/host tool call and the Mistral tool call with their results.
	history := []api.Message{
		{Role: api.MessageRoleUser, Content: "Earlier turn"},
		{
			Role:      api.MessageRoleAssistant,
			ToolCalls: []api.ToolCall{{ID: "call_legacy_non9", WireID: "call_legacy_non9", Name: "read", Args: map[string]any{"path": "old.txt"}}},
		},
		{Role: api.MessageRoleTool, Content: "old content", ToolResult: &api.ToolResult{ToolCallID: "call_legacy_non9"}},
		{Role: api.MessageRoleUser, Content: "Read README.md"},
		{Role: api.MessageRoleAssistant, ToolCalls: comp1.ToolCalls},
		{Role: api.MessageRoleTool, Content: "# Painted Wolf", ToolResult: &api.ToolResult{ToolCallID: comp1.ToolCalls[0].ID}},
	}
	ch2, err := provider.Stream(context.Background(), modelcall.CompletionRequest{
		Model:    "mistral-large-latest",
		Messages: history,
		Tools:    []tools.ToolMeta{{Name: "read", Description: "Read file"}},
	})
	if err != nil {
		t.Fatalf("Turn 2 Stream: %v", err)
	}
	comp2, _, err := modelcall.CollectStream(ch2)
	if err != nil {
		t.Fatalf("Turn 2 CollectStream: %v", err)
	}
	if comp2.Content != "File read complete." {
		t.Fatalf("Turn 2 answer = %q, want %q", comp2.Content, "File read complete.")
	}
	if comp2.Usage.PromptTokens != 68 || comp2.Usage.CompletionTokens != 5 {
		t.Fatalf("Turn 2 usage = %+v", comp2.Usage)
	}
}

func TestMistralCancellationAndProviderErrors(t *testing.T) {
	t.Run("rate limit 429", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"object":"error","message":"Rate limit exceeded","type":"rate_limited"}`))
		}))
		defer srv.Close()

		p := New("mistral", srv.URL+"/v1", "key", []modelinfo.Entry{{ID: "mistral-large-latest"}}).
			WithProfile(providerprofile.Mistral())
		_, err := p.Complete(context.Background(), modelcall.CompletionRequest{
			Model:    "mistral-large-latest",
			Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hi"}},
		})
		var rateErr *failure.ProviderRateLimitedError
		if !errors.As(err, &rateErr) {
			t.Fatalf("expected ProviderRateLimitedError, got %v", err)
		}
	})

	t.Run("canceled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		p := New("mistral", "http://127.0.0.1:1/v1", "key", []modelinfo.Entry{{ID: "mistral-large-latest"}}).
			WithProfile(providerprofile.Mistral())
		_, err := p.Complete(ctx, modelcall.CompletionRequest{
			Model:    "mistral-large-latest",
			Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hi"}},
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	})
}
