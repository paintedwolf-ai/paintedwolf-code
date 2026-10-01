package vertexexpress

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// vertexExpressSSEServer serves streamGenerateContent?alt=sse frames verbatim.
// Each frame contains one complete GenerateContentResponse.
func vertexExpressSSEServer(t *testing.T, frames []string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("expected flusher")
			return
		}
		for _, frame := range frames {
			fmt.Fprintf(w, "data: %s\n\n", frame)
			flusher.Flush()
		}
	}))
}

func vertexExpressStreamProvider(t *testing.T, frames []string) *Provider {
	t.Helper()
	server := vertexExpressSSEServer(t, frames)
	t.Cleanup(server.Close)
	return New("test", server.URL, "test-key", []modelinfo.Entry{{ID: "gemini-2.5-flash"}})
}

func TestVertexExpressStreamHappyPath(t *testing.T) {
	provider := vertexExpressStreamProvider(t, []string{
		`{"candidates":[{"content":{"role":"model","parts":[{"text":"Hello"}]}}],"usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":1}}`,
		`{"candidates":[{"content":{"role":"model","parts":[{"text":" world"}]}}],"usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":5}}`,
		`{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":9}}`,
	})

	ch, err := provider.Stream(context.Background(), modelcall.CompletionRequest{
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "Hello"}},
	})
	testutil.FailErr(t, "provider.Stream failed", err)
	completion, tokens, err := modelcall.CollectStream(ch)
	testutil.FailErr(t, "CollectStream failed", err)

	if completion.Content != "Hello world" {
		t.Fatalf("content = %q", completion.Content)
	}
	if len(tokens) != 2 || tokens[0] != "Hello" || tokens[1] != " world" {
		t.Fatalf("tokens = %v", tokens)
	}
	// The final frame contains the cumulative usage.
	want := modelcall.TokenUsage{Present: true, PromptTokens: 12, CompletionTokens: 9}
	if completion.Usage != want {
		t.Fatalf("usage = %+v, want %+v", completion.Usage, want)
	}
}

func TestVertexExpressStreamRejectsMalformedSSE(t *testing.T) {
	provider := vertexExpressStreamProvider(t, []string{`{"candidates":`})
	ch, err := provider.Stream(t.Context(), modelcall.CompletionRequest{})
	testutil.FailErr(t, "start malformed stream", err)
	if _, _, err := modelcall.CollectStream(ch); err == nil || !strings.Contains(err.Error(), "malformed SSE") {
		t.Fatalf("error = %v, want malformed SSE", err)
	}
}

// Thought parts carry chain-of-thought and must land on Reasoning, never in the
// content that reaches the transcript.
func TestVertexExpressStreamThoughtPartRoutedAsReasoning(t *testing.T) {
	provider := vertexExpressStreamProvider(t, []string{
		`{"candidates":[{"content":{"role":"model","parts":[{"text":"weighing options","thought":true}]}}]}`,
		`{"candidates":[{"content":{"role":"model","parts":[{"text":"the answer"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":4,"thoughtsTokenCount":6}}`,
	})

	ch, err := provider.Stream(context.Background(), modelcall.CompletionRequest{
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "think"}},
	})
	testutil.FailErr(t, "provider.Stream failed", err)
	completion, _, err := modelcall.CollectStream(ch)
	testutil.FailErr(t, "CollectStream failed", err)

	if completion.Content != "the answer" {
		t.Fatalf("content = %q, want the thought part excluded", completion.Content)
	}
	if completion.Reasoning != "weighing options" {
		t.Fatalf("reasoning = %q", completion.Reasoning)
	}
	// Thinking tokens bill as output but are reported separately.
	if completion.Usage.CompletionTokens != 10 {
		t.Fatalf("completion tokens = %d, want 4 candidates + 6 thoughts", completion.Usage.CompletionTokens)
	}
}

// Function calls arrive whole, so the terminal chunk carries a parsed call.
func TestVertexExpressStreamFunctionCall(t *testing.T) {
	provider := vertexExpressStreamProvider(t, []string{
		`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"read","args":{"path":"main.go"}}}]}}]}`,
		`{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":3}}`,
	})

	ch, err := provider.Stream(context.Background(), modelcall.CompletionRequest{
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "read main.go"}},
	})
	testutil.FailErr(t, "provider.Stream failed", err)
	completion, _, err := modelcall.CollectStream(ch)
	testutil.FailErr(t, "CollectStream failed", err)

	if len(completion.ToolCalls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(completion.ToolCalls))
	}
	call := completion.ToolCalls[0]
	if call.Name != "read" {
		t.Errorf("name = %q, want read", call.Name)
	}
	if got := call.Args["path"]; got != "main.go" {
		t.Errorf("args[path] = %v, want main.go", got)
	}
	// The wire pairs function responses by name.
	if call.ID == "" {
		t.Error("host tool-call id must be minted")
	}
	if call.WireID != "" {
		t.Errorf("WireID = %q, want empty (Gemini functionCall carries no id)", call.WireID)
	}
}

// Two calls in one turn must both survive to the terminal chunk.
func TestVertexExpressStreamParallelFunctionCalls(t *testing.T) {
	provider := vertexExpressStreamProvider(t, []string{
		`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"read","args":{"path":"a.go"}}},{"functionCall":{"name":"read","args":{"path":"b.go"}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":6}}`,
	})

	ch, err := provider.Stream(context.Background(), modelcall.CompletionRequest{
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "read both"}},
	})
	testutil.FailErr(t, "provider.Stream failed", err)
	completion, _, err := modelcall.CollectStream(ch)
	testutil.FailErr(t, "CollectStream failed", err)

	if len(completion.ToolCalls) != 2 {
		t.Fatalf("tool calls = %d, want 2", len(completion.ToolCalls))
	}
	if a, b := completion.ToolCalls[0].Args["path"], completion.ToolCalls[1].Args["path"]; a != "a.go" || b != "b.go" {
		t.Fatalf("args = %v, %v want a.go, b.go", a, b)
	}
	if completion.ToolCalls[0].ID == completion.ToolCalls[1].ID {
		t.Error("parallel calls must get distinct host ids")
	}
}

// A filtered stream ends cleanly with no parts — the terminal chunk must carry
// the reason rather than closing on a silent empty turn.
func TestVertexExpressStreamSurfacesContentFilterBlock(t *testing.T) {
	provider := vertexExpressStreamProvider(t, []string{
		`{"candidates":[{"content":{"role":"model"},"finishReason":"SAFETY"}],"usageMetadata":{"promptTokenCount":9}}`,
	})

	ch, err := provider.Stream(context.Background(), modelcall.CompletionRequest{
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hi"}},
	})
	testutil.FailErr(t, "provider.Stream failed", err)
	_, _, collectErr := modelcall.CollectStream(ch)
	if collectErr == nil {
		t.Fatal("want the block surfaced as a stream error")
	}
	if !strings.Contains(collectErr.Error(), "SAFETY") {
		t.Errorf("error = %q, want the finishReason named", collectErr)
	}
}

// Thoughts are not deliverable output: a turn blocked after it finished
// thinking must still report, not close on an empty transcript.
func TestVertexExpressStreamThoughtsAloneDoNotCountAsOutput(t *testing.T) {
	provider := vertexExpressStreamProvider(t, []string{
		`{"candidates":[{"content":{"role":"model","parts":[{"text":"considering","thought":true}]}}]}`,
		`{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"PROHIBITED_CONTENT"}],"usageMetadata":{"promptTokenCount":3,"thoughtsTokenCount":11}}`,
	})

	ch, err := provider.Stream(context.Background(), modelcall.CompletionRequest{
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hi"}},
	})
	testutil.FailErr(t, "provider.Stream failed", err)
	if _, _, collectErr := modelcall.CollectStream(ch); collectErr == nil {
		t.Fatal("want an error: the turn produced reasoning but no answer")
	}
}

// Output followed by a non-STOP reason is the ordinary truncation case, not a
// block — the stream must still deliver what arrived.
func TestVertexExpressStreamKeepsTruncatedOutput(t *testing.T) {
	provider := vertexExpressStreamProvider(t, []string{
		`{"candidates":[{"content":{"role":"model","parts":[{"text":"as far as"}]}}]}`,
		`{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"MAX_TOKENS"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":4}}`,
	})

	ch, err := provider.Stream(context.Background(), modelcall.CompletionRequest{
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hi"}},
	})
	testutil.FailErr(t, "provider.Stream failed", err)
	completion, _, collectErr := modelcall.CollectStream(ch)
	testutil.FailErr(t, "CollectStream failed", collectErr)
	if completion.Content != "as far as" {
		t.Fatalf("content = %q, want the partial answer kept", completion.Content)
	}
}

func TestVertexExpressStreamRejectsUnparseableFrame(t *testing.T) {
	provider := vertexExpressStreamProvider(t, []string{
		`{"candidates":[{"content":{"role":"model","parts":[{"text":"one"}]}}]}`,
		`not json at all`,
		`{"candidates":[{"content":{"role":"model","parts":[{"text":" two"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":2}}`,
	})

	ch, err := provider.Stream(context.Background(), modelcall.CompletionRequest{
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hi"}},
	})
	testutil.FailErr(t, "provider.Stream failed", err)
	_, _, err = modelcall.CollectStream(ch)
	if err == nil || !strings.Contains(err.Error(), "malformed SSE payload") {
		t.Fatalf("CollectStream error = %v, want malformed SSE payload", err)
	}
}
