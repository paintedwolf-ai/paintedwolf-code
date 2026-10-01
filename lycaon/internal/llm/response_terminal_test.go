package llm

import (
	"testing"

	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerretry"
)

func TestResponseRetryRetainsTerminalProvenance(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		inner := &responseRetryStub{stream: func(int) (<-chan modelcall.StreamChunk, error) {
			return retryChunkStream(modelcall.StreamChunk{Err: &failure.ProviderEmptyCompletionError{Retryable: true, Terminal: terminal}, Done: true}), nil
		}}
		provider := &responseRetryProvider{inner: inner, policy: retryEmptyOncePolicy()}
		stream, err := provider.Stream(t.Context(), modelcall.CompletionRequest{Model: "model"})
		if err != nil {
			t.Fatalf("start response: %v", err)
		}
		_, _, err = modelcall.CollectStream(stream)
		empty, ok := failure.AsProviderEmptyCompletion(err)
		if !ok || empty.Terminal != terminal || empty.Attempts != 2 {
			t.Fatalf("terminal=%v error=%+v", terminal, err)
		}
		mapped, ok := failure.AsProviderEmptyCompletion(providerretry.TransportFaultError("p", "m", emptyCompletionFault(err), 2))
		if !ok || mapped.Terminal != terminal {
			t.Fatalf("transport lost terminal provenance: %+v", mapped)
		}
	}
}

func TestResponseRetryDistinguishesTerminalEmptyFromClosedStream(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		inner := &responseRetryStub{stream: func(int) (<-chan modelcall.StreamChunk, error) {
			if terminal {
				return retryChunkStream(modelcall.StreamChunk{Done: true}), nil
			}
			return retryChunkStream(), nil
		}}
		provider := &responseRetryProvider{inner: inner, policy: retryEmptyOncePolicy()}
		stream, err := provider.Stream(t.Context(), modelcall.CompletionRequest{Model: "model"})
		if err != nil {
			t.Fatalf("start response: %v", err)
		}
		_, _, err = modelcall.CollectStream(stream)
		empty, ok := failure.AsProviderEmptyCompletion(err)
		if !ok || empty.Terminal != terminal {
			t.Fatalf("terminal=%v error=%+v", terminal, err)
		}
	}
}
