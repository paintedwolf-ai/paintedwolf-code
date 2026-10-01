package llm

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/llm/providerretry"
	anthropicprovider "github.com/lycaon/lycaon/internal/llm/providers/anthropic"
	"github.com/lycaon/lycaon/internal/llm/providers/openaicompat"
	"github.com/lycaon/lycaon/pkg/api"
)

type responseRetryStub struct {
	observeRequest func(modelcall.CompletionRequest)
	completeCalls  int
	streamCalls    int
	complete       func(int) (*modelcall.Completion, error)
	stream         func(int) (<-chan modelcall.StreamChunk, error)
}

func (p *responseRetryStub) ID() string { return "retry-stub" }
func (p *responseRetryStub) Models() []modelcall.ModelInfo {
	return []modelcall.ModelInfo{{ID: "model"}}
}
func (p *responseRetryStub) Profile() providerprofile.Profile {
	return providerprofile.Default()
}
func (p *responseRetryStub) Complete(_ context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	if p.observeRequest != nil {
		p.observeRequest(req)
	}
	p.completeCalls++
	return p.complete(p.completeCalls)
}
func (p *responseRetryStub) Stream(_ context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	if p.observeRequest != nil {
		p.observeRequest(req)
	}
	p.streamCalls++
	return p.stream(p.streamCalls)
}

func retryEmptyOncePolicy() providerretry.ProviderHTTPRetry {
	return providerretry.ProviderHTTPRetry{
		Transport: &providerretry.ProviderTransportRetry{
			MaxRetries: 1,
			MaxWaitMs:  1,
			BackoffMs:  []int{1},
			Faults:     []providerretry.FaultKind{providerretry.FaultEmptyCompletion},
		},
	}
}

func retryChunkStream(chunks ...modelcall.StreamChunk) <-chan modelcall.StreamChunk {
	out := make(chan modelcall.StreamChunk, len(chunks))
	for _, chunk := range chunks {
		out <- chunk
	}
	close(out)
	return out
}

func TestResponseRetryCompleteRetriesEmptyThenReturnsPayload(t *testing.T) {
	inner := &responseRetryStub{complete: func(call int) (*modelcall.Completion, error) {
		if call == 1 {
			return &modelcall.Completion{}, &failure.ProviderEmptyCompletionError{Retryable: true}
		}
		return &modelcall.Completion{Content: "recovered"}, nil
	}}
	provider := &responseRetryProvider{inner: inner, policy: retryEmptyOncePolicy()}
	var observed []providerretry.RetryAttempt
	ctx := providerretry.WithRetryObserver(t.Context(), func(attempt providerretry.RetryAttempt) {
		observed = append(observed, attempt)
	})

	got, err := provider.Complete(ctx, modelcall.CompletionRequest{Model: "model"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got.Content != "recovered" || inner.completeCalls != 2 {
		t.Fatalf("completion = %+v calls = %d, want recovered after two attempts", got, inner.completeCalls)
	}
	if len(observed) != 1 || observed[0].Reason != providerretry.RetryReasonEmptyCompletion {
		t.Fatalf("observed retries = %+v, want one empty-completion retry", observed)
	}
}

func TestRegistryDecoratorAppliesResponseRetryPolicy(t *testing.T) {
	inner := &responseRetryStub{complete: func(call int) (*modelcall.Completion, error) {
		if call == 1 {
			return &modelcall.Completion{}, nil
		}
		return &modelcall.Completion{Content: "recovered"}, nil
	}}
	snapshot := newProviderRegistrySnapshot(providerRegistrySnapshotInput{
		Providers: map[string]modelcall.Provider{inner.ID(): inner},
		Entries: map[string]CatalogEntry{
			inner.ID(): {ID: inner.ID(), HTTPRetry: retryEmptyOncePolicy()},
		},
	})
	provider := (&Registry{}).decorateProvider(snapshot, inner)

	got, err := provider.Complete(t.Context(), modelcall.CompletionRequest{Model: "model"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got.Content != "recovered" || inner.completeCalls != 2 {
		t.Fatalf("completion = %+v calls = %d, want registry retry", got, inner.completeCalls)
	}
}

func TestResponseRetryStreamRetriesSuccessfulEmptyTurn(t *testing.T) {
	inner := &responseRetryStub{stream: func(call int) (<-chan modelcall.StreamChunk, error) {
		if call == 1 {
			return retryChunkStream(modelcall.StreamChunk{Done: true}), nil
		}
		return retryChunkStream(modelcall.StreamChunk{Content: "recovered", Done: true}), nil
	}}
	provider := &responseRetryProvider{inner: inner, policy: retryEmptyOncePolicy()}

	stream, err := provider.Stream(t.Context(), modelcall.CompletionRequest{Model: "model"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	got, _, err := modelcall.CollectStream(stream)
	if err != nil {
		t.Fatalf("CollectStream: %v", err)
	}
	if got.Content != "recovered" || inner.streamCalls != 2 {
		t.Fatalf("completion = %+v calls = %d, want recovered after two attempts", got, inner.streamCalls)
	}
}

func TestResponseRetryStreamRetriesSynchronousEmptyTurn(t *testing.T) {
	inner := &responseRetryStub{stream: func(call int) (<-chan modelcall.StreamChunk, error) {
		if call == 1 {
			return nil, &failure.ProviderEmptyCompletionError{Retryable: true}
		}
		return retryChunkStream(modelcall.StreamChunk{Content: "recovered", Done: true}), nil
	}}
	provider := &responseRetryProvider{inner: inner, policy: retryEmptyOncePolicy()}

	stream, err := provider.Stream(t.Context(), modelcall.CompletionRequest{Model: "model"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	got, _, err := modelcall.CollectStream(stream)
	if err != nil {
		t.Fatalf("CollectStream: %v", err)
	}
	if got.Content != "recovered" || inner.streamCalls != 2 {
		t.Fatalf("completion = %+v calls = %d, want recovered after two attempts", got, inner.streamCalls)
	}
}

func TestResponseRetryStreamCountsSynchronousFailureAfterEmptyStream(t *testing.T) {
	inner := &responseRetryStub{stream: func(call int) (<-chan modelcall.StreamChunk, error) {
		if call == 1 {
			return retryChunkStream(modelcall.StreamChunk{Done: true}), nil
		}
		return nil, &failure.ProviderEmptyCompletionError{Retryable: true}
	}}
	provider := &responseRetryProvider{inner: inner, policy: retryEmptyOncePolicy()}

	stream, err := provider.Stream(t.Context(), modelcall.CompletionRequest{Model: "model"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	_, _, err = modelcall.CollectStream(stream)
	empty, ok := failure.AsProviderEmptyCompletion(err)
	if !ok {
		t.Fatalf("error = %v, want ProviderEmptyCompletionError", err)
	}
	if empty.Attempts != 2 || inner.streamCalls != 2 {
		t.Fatalf("empty error = %+v calls = %d, want two total attempts", empty, inner.streamCalls)
	}
}

func TestResponseRetryStreamReportsExhaustedAttempts(t *testing.T) {
	inner := &responseRetryStub{stream: func(int) (<-chan modelcall.StreamChunk, error) {
		return retryChunkStream(modelcall.StreamChunk{
			Err:  &failure.ProviderEmptyCompletionError{Retryable: true},
			Done: true,
		}), nil
	}}
	provider := &responseRetryProvider{inner: inner, policy: retryEmptyOncePolicy()}

	stream, err := provider.Stream(t.Context(), modelcall.CompletionRequest{Model: "model"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	_, _, err = modelcall.CollectStream(stream)
	empty, ok := failure.AsProviderEmptyCompletion(err)
	if !ok {
		t.Fatalf("error = %v, want ProviderEmptyCompletionError", err)
	}
	if empty.Attempts != 2 || empty.ProviderID != "retry-stub" || empty.Model != "model" {
		t.Fatalf("empty error = %+v, want provider/model and two attempts", empty)
	}
}

func TestResponseRetryDoesNotReplayStructuredRefusal(t *testing.T) {
	inner := &responseRetryStub{stream: func(int) (<-chan modelcall.StreamChunk, error) {
		return retryChunkStream(modelcall.StreamChunk{
			Err: &failure.ProviderEmptyCompletionError{
				Reason:    "SAFETY",
				Retryable: false,
			},
			Done: true,
		}), nil
	}}
	provider := &responseRetryProvider{inner: inner, policy: retryEmptyOncePolicy()}

	stream, err := provider.Stream(t.Context(), modelcall.CompletionRequest{Model: "model"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	_, _, err = modelcall.CollectStream(stream)
	empty, ok := failure.AsProviderEmptyCompletion(err)
	if !ok || empty.Reason != "SAFETY" {
		t.Fatalf("error = %v, want structured SAFETY terminal", err)
	}
	if inner.streamCalls != 1 {
		t.Fatalf("calls = %d, want no replay of structured refusal", inner.streamCalls)
	}
}

func TestResponseRetryDoesNotReplayAfterVisibleOutput(t *testing.T) {
	tests := []struct {
		name  string
		first modelcall.StreamChunk
	}{
		{name: "text", first: modelcall.StreamChunk{Content: "partial"}},
		{name: "tool call", first: modelcall.StreamChunk{ToolCalls: []api.ToolCall{{}}, Progress: true}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			inner := &responseRetryStub{stream: func(int) (<-chan modelcall.StreamChunk, error) {
				return retryChunkStream(
					tc.first,
					modelcall.StreamChunk{Err: &failure.ProviderEmptyCompletionError{Retryable: true}, Done: true},
				), nil
			}}
			provider := &responseRetryProvider{inner: inner, policy: retryEmptyOncePolicy()}

			stream, err := provider.Stream(t.Context(), modelcall.CompletionRequest{Model: "model"})
			if err != nil {
				t.Fatalf("Stream: %v", err)
			}
			_, _, err = modelcall.CollectStream(stream)
			if !errors.Is(err, failure.ErrProviderEmptyCompletion) {
				t.Fatalf("error = %v, want original terminal error", err)
			}
			if inner.streamCalls != 1 {
				t.Fatalf("calls = %d, want no replay after visible output", inner.streamCalls)
			}
		})
	}
}

func TestResponseRetryHonorsExplicitFaultCoverage(t *testing.T) {
	inner := &responseRetryStub{complete: func(int) (*modelcall.Completion, error) {
		return nil, &failure.ProviderEmptyCompletionError{Retryable: true}
	}}
	policy := retryEmptyOncePolicy()
	policy.Transport.Faults = []providerretry.FaultKind{providerretry.FaultUnreachable}
	provider := &responseRetryProvider{inner: inner, policy: policy}

	_, err := provider.Complete(t.Context(), modelcall.CompletionRequest{Model: "model"})
	if !errors.Is(err, failure.ErrProviderEmptyCompletion) {
		t.Fatalf("error = %v, want empty completion", err)
	}
	if inner.completeCalls != 1 {
		t.Fatalf("calls = %d, want policy to suppress retry", inner.completeCalls)
	}
}

func TestEmptyCompletionRetryabilityUsesStructuredProviderReasons(t *testing.T) {
	tests := []struct {
		name string
		got  bool
		want bool
	}{
		{name: "OpenAI clean stop", got: openaicompat.EmptyCompletionRetryable("stop"), want: true},
		{name: "OpenAI content filter", got: openaicompat.EmptyCompletionRetryable("content_filter"), want: false},
		{name: "OpenAI unknown future reason", got: openaicompat.EmptyCompletionRetryable("future_reason"), want: false},
		{name: "Anthropic clean end", got: anthropicprovider.EmptyCompletionRetryable("end_turn"), want: true},
		{name: "Anthropic refusal", got: anthropicprovider.EmptyCompletionRetryable("refusal"), want: false},
		{name: "Anthropic unknown future reason", got: anthropicprovider.EmptyCompletionRetryable("future_reason"), want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Fatalf("retryable = %v, want %v", tc.got, tc.want)
			}
		})
	}
}
