package failure

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"
)

func TestProviderToolCallUnparsedKeepsOriginalError(t *testing.T) {
	cause := errors.New("ollama: unexpected end of JSON input")
	err := fmt.Errorf("stream: %w", &ProviderToolCallUnparsedError{ProviderID: "desktop", Model: "m", Cause: cause})
	got, ok := AsProviderToolCallUnparsed(err)
	if !ok || got.ProviderID != "desktop" || !errors.Is(err, ErrProviderToolCallUnparsed) || !errors.Is(err, cause) {
		t.Fatalf("typed failure lost through wrapping: %v", err)
	}
	if err.Error() != "stream: ollama: unexpected end of JSON input" {
		t.Fatalf("message = %q, want the provider's original text", err.Error())
	}
	var empty *ProviderToolCallUnparsedError
	if empty.Error() != ErrProviderToolCallUnparsed.Error() || empty.Unwrap() != nil {
		t.Fatal("nil failure must describe itself without a cause")
	}
	if _, ok := AsProviderToolCallUnparsed(cause); ok {
		t.Fatal("untyped provider error classified as a dropped call")
	}
}

func TestClassifyStreamEndAndInterruptedResponse(t *testing.T) {
	ctx := t.Context()
	existing := InterruptedResponse(ctx, "p1", "m1", &ProviderResponseInterruptedError{Cause: io.ErrUnexpectedEOF})
	got, ok := AsProviderResponseInterrupted(existing)
	if !ok || got.ProviderID != "p1" || got.Model != "m1" {
		t.Fatalf("got = %+v, want provider and model populated on existing interrupted error", got)
	}

	if err := ClassifyStreamEnd(ctx, "p1", "m1", nil, true, true, "", true); err != nil {
		t.Fatalf("sawTerminal should return nil, got %v", err)
	}
	if err := ClassifyStreamEnd(ctx, "p1", "m1", io.ErrUnexpectedEOF, false, false, "", true); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("scanErr should return InterruptedResponse, got %v", err)
	}
	if err := ClassifyStreamEnd(ctx, "p1", "m1", nil, false, true, "", true); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("sawOutput without terminal should return InterruptedResponse, got %v", err)
	}
	emptyErr := ClassifyStreamEnd(ctx, "p1", "m1", nil, false, false, "empty", true)
	if empty, ok := AsProviderEmptyCompletion(emptyErr); !ok || !empty.Retryable || empty.Reason != "empty" {
		t.Fatalf("empty stream should return ProviderEmptyCompletionError, got %v", emptyErr)
	}

	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()
	if err := ClassifyStreamEnd(canceledCtx, "p1", "m1", nil, false, false, "empty", true); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled empty stream should return context.Canceled, got %v", err)
	}
}
