package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerretry"
	anthropicprovider "github.com/lycaon/lycaon/internal/llm/providers/anthropic"
	"github.com/lycaon/lycaon/internal/llm/providers/openaicompat"
	"github.com/lycaon/lycaon/internal/llm/providers/vertexexpress"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func retryInterruptedOncePolicy() providerretry.ProviderHTTPRetry {
	return providerretry.ProviderHTTPRetry{
		Transport: &providerretry.ProviderTransportRetry{
			MaxRetries: 1,
			MaxWaitMs:  1,
			BackoffMs:  []int{1},
		},
	}
}

func TestResponseRetryStreamReplaysInterruptedTurnAndResetsConsumer(t *testing.T) {
	inner := &responseRetryStub{stream: func(call int) (<-chan modelcall.StreamChunk, error) {
		if call == 1 {
			return retryChunkStream(
				modelcall.StreamChunk{
					Content:          "first partial ",
					Reasoning:        "abandoned thought",
					ReasoningDetails: []json.RawMessage{json.RawMessage(`{"type":"thinking"}`)},
					ToolCalls:        []api.ToolCall{{ID: "call-1", Name: "read"}},
					Progress:         true,
					Usage:            modelcall.TokenUsage{PromptTokens: 10, CompletionTokens: 4, Present: true},
				},
				modelcall.StreamChunk{
					Err:  &failure.ProviderResponseInterruptedError{ProviderID: "retry-stub", Model: "model", Cause: io.ErrUnexpectedEOF},
					Done: true,
				},
			), nil
		}
		return retryChunkStream(
			modelcall.StreamChunk{Content: "second ", Reasoning: "final thought"},
			modelcall.StreamChunk{
				Content: "complete",
				Usage:   modelcall.TokenUsage{PromptTokens: 12, CompletionTokens: 6, Present: true},
				Done:    true,
			},
		), nil
	}}
	provider := &responseRetryProvider{inner: inner, policy: retryInterruptedOncePolicy()}

	var observed []providerretry.RetryAttempt
	ctx := providerretry.WithRetryObserver(t.Context(), func(attempt providerretry.RetryAttempt) {
		observed = append(observed, attempt)
	})

	stream, err := provider.Stream(ctx, modelcall.CompletionRequest{Model: "model"})
	testutil.FailErr(t, "open stream", err)

	var chunks []modelcall.StreamChunk
	forwarded := make(chan modelcall.StreamChunk, 16)
	for chunk := range stream {
		chunks = append(chunks, chunk)
		forwarded <- chunk
	}
	close(forwarded)

	sawReset := false
	for _, chunk := range chunks {
		if chunk.ResetAttempt() {
			sawReset = true
		}
	}
	if !sawReset {
		t.Fatalf("chunks = %+v, want reset chunk before replay", chunks)
	}

	got, tokens, err := modelcall.CollectStream(forwarded)
	testutil.FailErr(t, "collect replayed stream", err)
	if inner.streamCalls != 2 {
		t.Fatalf("streamCalls = %d, want 2", inner.streamCalls)
	}
	if got.Content != "second complete" {
		t.Fatalf("content = %q, want only second attempt content", got.Content)
	}
	if len(tokens) != 2 || tokens[0] != "second " || tokens[1] != "complete" {
		t.Fatalf("tokens = %v, want only second attempt tokens", tokens)
	}
	if got.Reasoning != "final thought" || len(got.ReasoningDetails) != 0 || len(got.ToolCalls) != 0 {
		t.Fatalf("completion = %+v, want first attempt reasoning and partial tool progress cleared", got)
	}
	wantUsage := modelcall.TokenUsage{PromptTokens: 22, CompletionTokens: 10, Present: true}
	if got.Usage != wantUsage {
		t.Fatalf("usage = %+v, want summed usage %+v", got.Usage, wantUsage)
	}
	if len(observed) != 1 || observed[0].Reason != providerretry.RetryReasonInterrupted {
		t.Fatalf("observed retries = %+v, want one interrupted retry", observed)
	}
}

func TestResponseRetryStreamSecondInterruptionSurfacesTypedError(t *testing.T) {
	inner := &responseRetryStub{stream: func(call int) (<-chan modelcall.StreamChunk, error) {
		return retryChunkStream(
			modelcall.StreamChunk{
				Content: "partial",
				Usage:   modelcall.TokenUsage{PromptTokens: 5, CompletionTokens: 2, Present: true},
			},
			modelcall.StreamChunk{
				Err:  &failure.ProviderResponseInterruptedError{Cause: io.ErrUnexpectedEOF},
				Done: true,
			},
		), nil
	}}
	provider := &responseRetryProvider{inner: inner, policy: retryInterruptedOncePolicy()}

	stream, err := provider.Stream(t.Context(), modelcall.CompletionRequest{Model: "model"})
	testutil.FailErr(t, "open stream", err)
	got, _, err := modelcall.CollectStream(stream)
	interrupted, ok := failure.AsProviderResponseInterrupted(err)
	if !ok || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("err = %v, want ProviderResponseInterruptedError wrapping unexpected EOF", err)
	}
	if interrupted.ProviderID != "retry-stub" || interrupted.Model != "model" {
		t.Fatalf("interrupted = %+v, want provider and model populated", interrupted)
	}
	if inner.streamCalls != 2 {
		t.Fatalf("streamCalls = %d, want 2", inner.streamCalls)
	}
	wantUsage := modelcall.TokenUsage{PromptTokens: 10, CompletionTokens: 4, Present: true}
	if got == nil || got.Usage != wantUsage {
		t.Fatalf("completion = %+v, want summed usage %+v", got, wantUsage)
	}
}

func TestResponseRetryStreamDoesNotReplayAfterCompletedToolCall(t *testing.T) {
	inner := &responseRetryStub{stream: func(int) (<-chan modelcall.StreamChunk, error) {
		return retryChunkStream(
			modelcall.StreamChunk{
				ToolCalls: []api.ToolCall{{ID: "call-1", Name: "write", Args: map[string]any{"path": "a.txt"}}},
				Progress:  false,
			},
			modelcall.StreamChunk{
				Err:  &failure.ProviderResponseInterruptedError{Cause: io.ErrUnexpectedEOF},
				Done: true,
			},
		), nil
	}}
	provider := &responseRetryProvider{inner: inner, policy: retryInterruptedOncePolicy()}

	stream, err := provider.Stream(t.Context(), modelcall.CompletionRequest{Model: "model"})
	testutil.FailErr(t, "open stream", err)
	_, _, err = modelcall.CollectStream(stream)
	if _, ok := failure.AsProviderResponseInterrupted(err); !ok {
		t.Fatalf("err = %v, want ProviderResponseInterruptedError", err)
	}
	if inner.streamCalls != 1 {
		t.Fatalf("streamCalls = %d, want 1 (no replay after completed tool call)", inner.streamCalls)
	}
}

func TestResponseRetryDoesNotReplayCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	inner := &responseRetryStub{
		complete: func(int) (*modelcall.Completion, error) {
			cancel()
			return nil, &failure.ProviderResponseInterruptedError{Cause: io.ErrUnexpectedEOF}
		},
		stream: func(int) (<-chan modelcall.StreamChunk, error) {
			cancel()
			return retryChunkStream(
				modelcall.StreamChunk{Content: "partial"},
				modelcall.StreamChunk{Err: &failure.ProviderResponseInterruptedError{Cause: io.ErrUnexpectedEOF}, Done: true},
			), nil
		},
	}
	provider := &responseRetryProvider{inner: inner, policy: retryInterruptedOncePolicy()}

	_, err := provider.Complete(ctx, modelcall.CompletionRequest{Model: "model"})
	if !errors.Is(err, context.Canceled) || inner.completeCalls != 1 {
		t.Fatalf("Complete err = %v calls = %d, want context.Canceled after 1 call", err, inner.completeCalls)
	}

	ctx2, cancel2 := context.WithCancel(t.Context())
	inner.streamCalls = 0
	inner.stream = func(int) (<-chan modelcall.StreamChunk, error) {
		cancel2()
		return retryChunkStream(
			modelcall.StreamChunk{Content: "partial"},
			modelcall.StreamChunk{Err: &failure.ProviderResponseInterruptedError{Cause: io.ErrUnexpectedEOF}, Done: true},
		), nil
	}
	stream, streamErr := provider.Stream(ctx2, modelcall.CompletionRequest{Model: "model"})
	testutil.FailErr(t, "open stream", streamErr)
	_, _, collectErr := modelcall.CollectStream(stream)
	if inner.streamCalls != 1 {
		t.Fatalf("streamCalls = %d, want 1 when context is canceled", inner.streamCalls)
	}
	_ = collectErr
}

func TestResponseRetryCompleteReplaysInterruptedCallAndSumsUsage(t *testing.T) {
	inner := &responseRetryStub{complete: func(call int) (*modelcall.Completion, error) {
		if call == 1 {
			return &modelcall.Completion{
				Usage: modelcall.TokenUsage{PromptTokens: 8, CompletionTokens: 3, Present: true},
			}, &failure.ProviderResponseInterruptedError{Cause: io.ErrUnexpectedEOF}
		}
		return &modelcall.Completion{
			Content: "recovered completion",
			Usage:   modelcall.TokenUsage{PromptTokens: 8, CompletionTokens: 7, Present: true},
		}, nil
	}}
	provider := &responseRetryProvider{inner: inner, policy: retryInterruptedOncePolicy()}

	var observed []providerretry.RetryAttempt
	ctx := providerretry.WithRetryObserver(t.Context(), func(attempt providerretry.RetryAttempt) {
		observed = append(observed, attempt)
	})

	got, err := provider.Complete(ctx, modelcall.CompletionRequest{Model: "model"})
	testutil.FailErr(t, "Complete after interrupted retry", err)
	if inner.completeCalls != 2 || got.Content != "recovered completion" {
		t.Fatalf("got = %+v calls = %d, want recovered completion after 2 calls", got, inner.completeCalls)
	}
	wantUsage := modelcall.TokenUsage{PromptTokens: 16, CompletionTokens: 10, Present: true}
	if got.Usage != wantUsage {
		t.Fatalf("usage = %+v, want summed usage %+v", got.Usage, wantUsage)
	}
	if len(observed) != 1 || observed[0].Reason != providerretry.RetryReasonInterrupted {
		t.Fatalf("observed = %+v, want one interrupted retry", observed)
	}
}

func TestResponseRetryStreamHandlesSynchronousAndChannelCloseInterruptions(t *testing.T) {
	t.Run("synchronous stream interruption then success", func(t *testing.T) {
		inner := &responseRetryStub{stream: func(call int) (<-chan modelcall.StreamChunk, error) {
			if call == 1 {
				return nil, &failure.ProviderResponseInterruptedError{Cause: io.ErrUnexpectedEOF}
			}
			return retryChunkStream(modelcall.StreamChunk{Content: "recovered", Done: true}), nil
		}}
		provider := &responseRetryProvider{inner: inner, policy: retryInterruptedOncePolicy()}
		stream, err := provider.Stream(t.Context(), modelcall.CompletionRequest{Model: "model"})
		testutil.FailErr(t, "open stream", err)
		got, _, err := modelcall.CollectStream(stream)
		testutil.FailErr(t, "collect stream", err)
		if got.Content != "recovered" || inner.streamCalls != 2 {
			t.Fatalf("got = %+v calls = %d, want recovered after 2 calls", got, inner.streamCalls)
		}
	})

	t.Run("channel closes after output without terminal chunk", func(t *testing.T) {
		inner := &responseRetryStub{stream: func(call int) (<-chan modelcall.StreamChunk, error) {
			if call == 1 {
				return retryChunkStream(modelcall.StreamChunk{Content: "abandoned"}), nil
			}
			return nil, &failure.ProviderResponseInterruptedError{Cause: io.ErrUnexpectedEOF}
		}}
		provider := &responseRetryProvider{inner: inner, policy: retryInterruptedOncePolicy()}
		stream, err := provider.Stream(t.Context(), modelcall.CompletionRequest{Model: "model"})
		testutil.FailErr(t, "open stream", err)
		_, _, err = modelcall.CollectStream(stream)
		if _, ok := failure.AsProviderResponseInterrupted(err); !ok || inner.streamCalls != 2 {
			t.Fatalf("err = %v calls = %d, want ProviderResponseInterruptedError after 2 calls", err, inner.streamCalls)
		}
	})

	t.Run("tool diagnostic resets on attempt reset chunk", func(t *testing.T) {
		ch := retryChunkStream(
			modelcall.StreamChunk{
				Content:          "old",
				Reasoning:        "old-reason",
				ReasoningDetails: []json.RawMessage{json.RawMessage(`{}`)},
				ToolCalls:        []api.ToolCall{{ID: "1", Name: "read"}},
				Progress:         true,
			},
			modelcall.StreamChunk{Reset: true},
			modelcall.StreamChunk{Content: "new", Done: true},
		)
		got, err := collectToolDiagnostic(t.Context(), ch)
		testutil.FailErr(t, "collectToolDiagnostic", err)
		if got.Content != "new" || got.Reasoning != "" || len(got.ReasoningDetails) != 0 || len(got.ToolCalls) != 0 {
			t.Fatalf("got = %+v, want prior attempt cleared", got)
		}
	})
}

func TestDriversRecoverFromMidStreamResetAcrossProviders(t *testing.T) {
	abortMidStream := func(w http.ResponseWriter, payload string) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Content-Length", "4096")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(payload))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		if hijacker, ok := w.(http.Hijacker); ok {
			if conn, _, err := hijacker.Hijack(); err == nil {
				if tcp, ok := conn.(*net.TCPConn); ok {
					_ = tcp.SetLinger(0)
				}
				_ = conn.Close()
			}
		}
	}

	req := modelcall.CompletionRequest{
		Model:    "model-1",
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hello"}},
	}

	cases := []struct {
		name  string
		build func(t *testing.T, attempts *atomic.Int32) modelcall.Provider
	}{
		{
			name: "fireworks-openaicompat",
			build: func(t *testing.T, attempts *atomic.Int32) modelcall.Provider {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if attempts.Add(1) == 1 {
						abortMidStream(w, "data: {\"choices\":[{\"delta\":{\"content\":\"discarded \"}}]}\n\n")
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"recovered\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
				}))
				t.Cleanup(srv.Close)
				return openaicompat.New("fireworks", srv.URL, "key", []modelinfo.Entry{{ID: "model-1"}})
			},
		},
		{
			name: "anthropic",
			build: func(t *testing.T, attempts *atomic.Int32) modelcall.Provider {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if attempts.Add(1) == 1 {
						abortMidStream(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"discarded \"}}\n\n")
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = w.Write([]byte("data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"recovered\"}}\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\ndata: {\"type\":\"message_stop\"}\n\n"))
				}))
				t.Cleanup(srv.Close)
				return anthropicprovider.New("anthropic", srv.URL, "key", []modelinfo.Entry{{ID: "model-1"}})
			},
		},
		{
			name: "vertexexpress",
			build: func(t *testing.T, attempts *atomic.Int32) modelcall.Provider {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if attempts.Add(1) == 1 {
						abortMidStream(w, "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"discarded \"}]}}]}\n\n")
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = w.Write([]byte("data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"recovered\"}]},\"finishReason\":\"STOP\"}]}\n\n"))
				}))
				t.Cleanup(srv.Close)
				return vertexexpress.New("vertex-express", srv.URL+"/v1", "key", []modelinfo.Entry{{ID: "model-1"}})
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var attempts atomic.Int32
			inner := tc.build(t, &attempts)
			snapshot := newProviderRegistrySnapshot(providerRegistrySnapshotInput{
				Providers: map[string]modelcall.Provider{inner.ID(): inner},
				Entries: map[string]CatalogEntry{
					inner.ID(): {ID: inner.ID(), HTTPRetry: retryInterruptedOncePolicy()},
				},
			})
			decorated := (&Registry{outboundSecretScreen: NewModelSecretScreen(nil, nil)}).decorateProvider(snapshot, inner)

			var progressSnapshots []string
			stream, err := decorated.Stream(t.Context(), req)
			testutil.FailErr(t, "open decorated stream", err)
			got, tokens, err := modelcall.CollectStreamWithProgress(stream, func(partial *modelcall.Completion) {
				if partial != nil {
					progressSnapshots = append(progressSnapshots, partial.Content)
				}
			})
			testutil.FailErr(t, "collect decorated stream", err)
			if attempts.Load() != 2 {
				t.Fatalf("attempts = %d, want 2", attempts.Load())
			}
			if got.Content != "recovered" {
				t.Fatalf("content = %q, want %q", got.Content, "recovered")
			}
			if len(tokens) != 1 || tokens[0] != "recovered" {
				t.Fatalf("tokens = %v, want [recovered]", tokens)
			}
			if len(progressSnapshots) == 0 || progressSnapshots[len(progressSnapshots)-1] != "recovered" {
				t.Fatalf("progressSnapshots = %v, want final snapshot 'recovered'", progressSnapshots)
			}
		})
	}
}
