package llm

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/noticeerr"
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
