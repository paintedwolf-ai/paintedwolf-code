package openaicompat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/httpclient"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerhttp"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestAcceptedEmptyStreamIsNeverReissued(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintln(w, "data: [DONE]")
	}))
	defer server.Close()
	p := New("test", server.URL, "key", []modelinfo.Entry{{ID: "m"}})
	ch, err := p.Stream(context.Background(), modelcall.CompletionRequest{Model: "m"})
	testutil.FailErr(t, "Stream", err)
	_, _, err = modelcall.CollectStream(ch)
	if err == nil {
		t.Fatal("expected empty accepted stream to fail")
	}
	if attempts.Load() != 1 {
		t.Fatalf("attempts = %d, want exactly one accepted request", attempts.Load())
	}
}

func TestMalformedStreamPayloadFailsClosed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintln(w, "data: {broken")
	}))
	defer server.Close()
	p := New("test", server.URL, "key", []modelinfo.Entry{{ID: "m"}})
	ch, err := p.Stream(context.Background(), modelcall.CompletionRequest{Model: "m"})
	testutil.FailErr(t, "Stream", err)
	_, _, err = modelcall.CollectStream(ch)
	if err == nil {
		t.Fatal("expected malformed stream payload to fail")
	}
}

func TestAcceptedJSONStreamResponseUsesSameRequest(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": "same response"}}},
			"usage":   map[string]any{"prompt_tokens": 3, "completion_tokens": 2},
		})
	}))
	defer server.Close()
	p := New("test", server.URL, "key", []modelinfo.Entry{{ID: "m"}})
	ch, err := p.Stream(context.Background(), modelcall.CompletionRequest{Model: "m"})
	testutil.FailErr(t, "Stream", err)
	completion, _, err := modelcall.CollectStream(ch)
	testutil.FailErr(t, "CollectStream", err)
	if completion.Content != "same response" || completion.Usage.PromptTokens != 3 || completion.Usage.CompletionTokens != 2 {
		t.Fatalf("completion = %+v", completion)
	}
	if attempts.Load() != 1 {
		t.Fatalf("attempts = %d, want one accepted request", attempts.Load())
	}
}

func TestJSONStreamFallbackHonorsCompletionBodyLimit(t *testing.T) {
	p := New("test", "http://unused", "key", []modelinfo.Entry{{ID: "m"}})
	resp := &http.Response{
		Body:          io.NopCloser(strings.NewReader(`{"choices":[]}`)),
		ContentLength: providerhttp.MaxCompletionResponseBodyBytes.Int64() + 1,
	}
	ch, err := p.streamFromJSONResponse(context.Background(), modelcall.CompletionRequest{Model: "m"}, resp)
	testutil.FailErr(t, "streamFromJSONResponse", err)
	_, _, err = modelcall.CollectStream(ch)
	if !errors.Is(err, httpclient.ErrResponseBodyTooLarge) {
		t.Fatalf("error = %v, want completion response-body limit", err)
	}
}
