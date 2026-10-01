package llm

import (
	"testing"

	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
)

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
