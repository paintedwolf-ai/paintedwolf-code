package commandprogram

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestStreamInputAndNullEndpointsPreserveIngressLimits(t *testing.T) {
	want := []byte{'a', 0, '\r', '\n', 0xff}
	budget, err := NewByteBudget("input_bytes", int64(len(want)))
	testutil.FailErr(t, "input budget", err)
	input, err := NewStreamInput(t.Context(), io.NopCloser(bytes.NewReader(want)), budget)
	testutil.FailErr(t, "create complete input", err)
	defer input.Close()
	got, err := io.ReadAll(input)
	testutil.FailErr(t, "read byte input", err)
	if !bytes.Equal(got, want) || input.Termination() != StreamEOF {
		t.Fatalf("input bytes/EOF changed: %v %s", got, input.Termination())
	}
	null := NullInput()
	defer null.Close()
	if n, err := null.Read(make([]byte, 1)); n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("null input=%d %v", n, err)
	}
	output := NullOutput{Context: t.Context(), Budget: budget}
	if n, err := output.Write(want); n != 0 || err == nil {
		t.Fatal("null output bypassed shared ingress budget")
	}
}

func TestInputCancellationClosesBlockedSource(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	reader, writer := io.Pipe()
	defer writer.Close()
	budget, err := NewByteBudget("input_bytes", 100)
	testutil.FailErr(t, "input budget", err)
	input, err := NewStreamInput(ctx, reader, budget)
	testutil.FailErr(t, "create blocked input", err)
	defer input.Close()
	done := make(chan error, 1)
	go func() { _, err := input.Read(make([]byte, 1)); done <- err }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("input cancellation=%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("input cancellation leaked blocked read")
	}
	if input.Termination() != StreamCanceled {
		t.Fatalf("input terminal=%s", input.Termination())
	}
}
