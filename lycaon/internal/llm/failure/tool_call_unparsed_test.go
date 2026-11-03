package failure

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	wire "github.com/lycaon/lycaon/pkg/api"
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

func TestProviderOutputTruncatedError(t *testing.T) {
	var empty *ProviderOutputTruncatedError
	if empty.OutputLimitReached() || empty.LowOutputCeiling() || empty.Error() != ErrProviderOutputTruncated.Error() {
		t.Fatalf("nil truncated error = %q", empty.Error())
	}
	err := &ProviderOutputTruncatedError{
		ProviderID:          "openrouter",
		Model:               "qwen/qwen3.6-plus",
		FinishReason:        "length",
		PromptTokens:        100,
		CompletionTokens:    4096,
		ContextTokens:       4196,
		MaxTokens:           4096,
		ConfiguredMaxTokens: 4096,
		ProviderMaxTokens:   65536,
		ToolCallTruncated:   true,
		ToolArgsBytes:       512,
	}
	if !err.OutputLimitReached() || !err.LowOutputCeiling() || err.NoticeCode() != wire.NoticeCodeProviderEmptyCompletion {
		t.Fatalf("unexpected truncated metadata: %+v", err)
	}
	if !strings.Contains(err.Error(), "tool=unknown args_bytes=512") || !strings.Contains(err.Error(), "configured_max_tokens=4096 provider_max_tokens=65536") {
		t.Fatalf("message missing truncated tool or ceiling details: %q", err.Error())
	}
	err.ToolName = "write_file"
	if !strings.Contains(err.Error(), "tool=write_file args_bytes=512") {
		t.Fatalf("message missing named tool details: %q", err.Error())
	}
	got, ok := AsProviderOutputTruncated(fmt.Errorf("wrap: %w", err))
	if !ok || got != err || !errors.Is(err, ErrProviderOutputTruncated) {
		t.Fatalf("AsProviderOutputTruncated failed: %v", got)
	}
	if _, ok := AsProviderOutputTruncated(errors.New("other")); ok {
		t.Fatal("unrelated error matched AsProviderOutputTruncated")
	}
}
