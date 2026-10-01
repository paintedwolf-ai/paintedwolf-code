package llm

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerretry"
	"github.com/lycaon/lycaon/internal/llm/providers/openaicompat"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestAvailabilityStreamPreservesCommittedHistoryAndControls(t *testing.T) {
	var mu sync.Mutex
	var requests [][]byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read provider request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		requests = append(requests, body)
		attempt := len(requests)
		mu.Unlock()
		if attempt <= 12 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"error":{"message":"Unavailable"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Complete.\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	provider := openaicompat.New("fixture", server.URL, "", nil).WithHTTPRetry(availabilityTestPolicy())
	request := modelcall.CompletionRequest{Model: "candidate", MaxTokens: 16384, Messages: []api.Message{
		{Role: api.MessageRoleUser, Content: "Apply the requested change."},
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{
			ID: "committed-write", Name: "write", Args: map[string]any{"path": "result.txt", "content": "saved"},
		}}},
		{Role: api.MessageRoleTool, Content: "saved", ToolResult: &api.ToolResult{ToolCallID: "committed-write", Content: "saved"}},
	}}
	stream, err := provider.Stream(t.Context(), request)
	testutil.FailErr(t, "recover stream after server errors", err)
	completion, _, err := modelcall.CollectStream(stream)
	testutil.FailErr(t, "collect recovered completion", err)
	mu.Lock()
	defer mu.Unlock()
	if completion.Content != "Complete." || len(requests) != 13 {
		t.Fatalf("content=%q requests=%d", completion.Content, len(requests))
	}
	for _, body := range requests[1:] {
		if !bytes.Equal(body, requests[0]) {
			t.Fatal("availability retry changed the admitted request")
		}
	}
	var wire struct {
		Messages  []json.RawMessage `json:"messages"`
		MaxTokens int               `json:"max_tokens"`
	}
	testutil.FailErr(t, "decode retained request", json.Unmarshal(requests[0], &wire))
	if len(wire.Messages) != 3 || wire.MaxTokens != request.MaxTokens {
		t.Fatalf("history=%d output allowance=%d", len(wire.Messages), wire.MaxTokens)
	}
}

func availabilityTestPolicy() providerretry.ProviderHTTPRetry {
	return providerretry.ProviderHTTPRetry{MaxRetries: 2, MaxWaitMs: 10, BackoffMs: []int{1, 1},
		Statuses: []int{429, 500, 502, 503, 504}, WaitHeaders: []string{"Retry-After"},
		Availability: &providerretry.ProviderAvailabilityRetry{InitialMs: 1, MaxBackoffMs: 2}}
}
