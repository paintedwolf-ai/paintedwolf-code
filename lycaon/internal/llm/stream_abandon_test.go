package llm

import (
	"context"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
)

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
	case <-time.After(2 * time.Second):
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
	case <-time.After(2 * time.Second):
		t.Fatal("provider goroutine stranded behind the stall guard")
	}
}
