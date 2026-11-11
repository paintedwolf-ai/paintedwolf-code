package llm

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providers/openaicompat"
	"github.com/lycaon/lycaon/internal/noticeerr"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"io"
	"testing"
	"time"
)

func TestResponseInterruptionPreservesCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := failure.InterruptedResponse(ctx, "fixture", "model", io.ErrUnexpectedEOF)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("caller cancellation changed: %v", err)
	}
	if _, ok := noticeerr.CodeOf(err); ok {
		t.Fatal("caller cancellation became a provider failure")
	}
}

func TestStreamCollectionPreservesHostScriptedProvenance(t *testing.T) {
	for _, progress := range []bool{false, true} {
		stream := make(chan modelcall.StreamChunk, 1)
		stream <- modelcall.StreamChunk{Content: "Prepared.", Scripted: true, Done: true}
		close(stream)
		var completion *modelcall.Completion
		var err error
		if progress {
			completion, _, err = modelcall.CollectStreamWithProgress(stream, nil)
		} else {
			completion, _, err = modelcall.CollectStream(stream)
		}
		testutil.FailErr(t, "collect scripted response", err)
		if !completion.Scripted {
			t.Fatal("host-scripted response lost provenance")
		}
	}
}

func TestCollectStreamWithProgressMergesToolSnapshots(t *testing.T) {
	ch := make(chan modelcall.StreamChunk, 4)
	go func() {
		ch <- modelcall.StreamChunk{ToolCalls: []api.ToolCall{{Name: "write", Args: map[string]any{"path": "a"}}}, Progress: true}
		ch <- modelcall.StreamChunk{ToolCalls: []api.ToolCall{{Name: "write", Args: map[string]any{"path": "a", "content": "hi"}}}, Progress: true}
		ch <- modelcall.StreamChunk{ToolCalls: []api.ToolCall{{Name: "write", Args: map[string]any{"path": "a", "content": "hello"}}}, Done: true}
		close(ch)
	}()
	var progress int
	completion, _, err := modelcall.CollectStreamWithProgress(ch, func(c *modelcall.Completion) {
		progress++
		if c == nil || len(c.ToolCalls) != 1 {
			t.Fatalf("partial = %+v", c)
		}
	})
	testutil.FailErr(t, "CollectStreamWithProgress failed", err)
	if progress < 2 {
		t.Fatalf("progress callbacks = %d", progress)
	}
	if len(completion.ToolCalls) != 1 {
		t.Fatalf("tool calls = %+v", completion.ToolCalls)
	}
}

func TestResponseExhaustionCannotRestartAtWorkerLayer(t *testing.T) {
	for _, mode := range []string{"complete", "stream start", "stream terminal"} {
		t.Run(mode, func(t *testing.T) {
			inner := &responseRetryStub{
				complete: func(int) (*modelcall.Completion, error) {
					return nil, &failure.ProviderEmptyCompletionError{Retryable: true}
				},
				stream: func(int) (<-chan modelcall.StreamChunk, error) {
					failure := &failure.ProviderEmptyCompletionError{Retryable: true}
					if mode == "stream start" {
						return nil, failure
					}
					return retryChunkStream(modelcall.StreamChunk{Err: failure, Done: true}), nil
				},
			}
			provider := &responseRetryProvider{inner: inner, policy: retryEmptyOncePolicy()}
			var err error
			if mode == "complete" {
				_, err = provider.Complete(t.Context(), modelcall.CompletionRequest{Model: "model"})
			} else {
				var stream <-chan modelcall.StreamChunk
				stream, err = provider.Stream(t.Context(), modelcall.CompletionRequest{Model: "model"})
				if err == nil {
					_, _, err = modelcall.CollectStream(stream)
				}
			}
			empty, ok := failure.AsProviderEmptyCompletion(err)
			if !ok || empty.Retryable || empty.Attempts != 2 {
				t.Fatalf("exhausted response=%+v err=%v", empty, err)
			}
			if inner.completeCalls+inner.streamCalls != 2 {
				t.Fatalf("requests=%d", inner.completeCalls+inner.streamCalls)
			}
		})
	}
}

// abandonSettle gives a canceled producer one scheduling window to exit.
const abandonSettle = 50 * time.Millisecond

// assertStreamClosed detects producers still parked on a send.
func assertStreamClosed(t *testing.T, ch <-chan modelcall.StreamChunk, what string) {
	t.Helper()
	select {
	case chunk, ok := <-ch:
		if ok {
			t.Fatalf("%s: producer was stranded on a send after the consumer abandoned the stream (delivered %+v)", what, chunk)
		}
	case <-t.Context().Done():
		t.Fatalf("%s: stream neither closed nor delivered after abandonment", what)
	}
}

// CollectStream returns without draining on the error path, so the stall
// guard's forwarding send selects on ctx.Done() and exits when the prompt loop's
// deferred cancel fires.
func TestGuardStreamStallReleasesAbandonedStream(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	// A real provider decoder blocks on a bare send; it does not watch ctx while
	// handing a chunk over. Three chunks is enough for one to be consumed, one to
	// park the guard, and one to park the provider behind it.
	producerDone := make(chan struct{})
	in := make(chan modelcall.StreamChunk)
	go func() {
		defer close(producerDone)
		defer close(in)
		for i := 0; i < 3; i++ {
			in <- modelcall.StreamChunk{Content: "x"}
		}
	}()

	out := modelcall.GuardStreamStall(ctx, in, time.Hour, nil)
	if _, ok := <-out; !ok {
		t.Fatal("expected at least one forwarded chunk")
	}
	// Abandon exactly as CollectStream's error path does: stop reading, never
	// drain. Then cancel, as the prompt loop's deferred cancelStall does.
	cancel()
	time.Sleep(abandonSettle)

	assertStreamClosed(t, out, "stall guard")

	select {
	case <-producerDone:
	case <-t.Context().Done():
		t.Fatal("provider goroutine stranded behind the stall guard")
	}
}

func TestEncodeResponseFormatJSONSchema(t *testing.T) {
	provider := openaicompat.New("openai", "https://api.openai.com/v1", "key", []modelinfo.Entry{
		{ID: "gpt-4.1-mini"},
	})
	req := modelcall.CompletionRequest{
		Model:          "gpt-4.1-mini",
		Messages:       []api.Message{{Role: api.MessageRoleUser, Content: "summarize"}},
		ResponseFormat: CurateResponseFormat(),
	}
	body, err := provider.Prepare(req, false)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	rf, ok := wire["response_format"].(map[string]any)
	if !ok {
		t.Fatalf("missing response_format: %s", body)
	}
	if rf["type"] != "json_schema" {
		t.Fatalf("type = %v want json_schema", rf["type"])
	}
	schemaObj, ok := rf["json_schema"].(map[string]any)
	if !ok {
		t.Fatalf("missing json_schema: %s", body)
	}
	if schemaObj["name"] != "curate" {
		t.Fatalf("name = %v", schemaObj["name"])
	}
	if schemaObj["strict"] != true {
		t.Fatalf("strict = %v want true", schemaObj["strict"])
	}
	schema, ok := schemaObj["schema"].(map[string]any)
	if !ok {
		t.Fatalf("schema missing: %s", body)
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok || props["selections"] == nil {
		t.Fatalf("schema properties incomplete: %s", body)
	}
}
