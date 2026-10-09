package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestHarnessResponsePreservesProviderStreamFailure(t *testing.T) {
	provider := llm.NewManualProvider()
	provider.SetAuto(false, "")
	stream, err := provider.Stream(t.Context(), modelcall.CompletionRequest{})
	testutil.FailErr(t, "start manual stream", err)
	pending, ok := provider.Pending(t.Context(), "", time.Second)
	if !ok {
		t.Fatal("manual request was not pending")
	}
	body, err := json.Marshal(map[string]any{
		"id": pending.ID,
		"stream_chunks": []map[string]any{
			{"content": "partial response"},
			{"error": "fixture provider disconnected", "done": true},
		},
	})
	testutil.FailErr(t, "encode failed response", err)
	server := &Server{manualLLM: provider}
	response := httptest.NewRecorder()
	server.handleHarnessLLMRespond(response, httptest.NewRequest(http.MethodPost, "/harness/llm/respond", strings.NewReader(string(body))))
	if response.Code != http.StatusOK {
		t.Fatalf("response = %d: %s", response.Code, response.Body.String())
	}
	first := <-stream
	if first.Content != "partial response" || first.Err != nil || first.Done {
		t.Fatalf("partial chunk = %+v", first)
	}
	last := <-stream
	if last.Err == nil || last.Err.Error() != "fixture provider disconnected" || !last.Done {
		t.Fatalf("terminal chunk = %+v", last)
	}
	if _, open := <-stream; open {
		t.Fatal("failed stream stayed open")
	}
}
