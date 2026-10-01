package observability

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

// gatedWriter blocks every Write until released, like a terminal that stopped draining.
type gatedWriter struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	release   chan struct{}
	entered   chan struct{}
	enterOnce sync.Once
}

func newGatedWriter() *gatedWriter {
	return &gatedWriter{release: make(chan struct{}), entered: make(chan struct{})}
}

func (w *gatedWriter) Write(p []byte) (int, error) {
	w.enterOnce.Do(func() { close(w.entered) })
	<-w.release
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *gatedWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

func TestConsoleSinkWriteReturnsWhileOutputIsStalled(t *testing.T) {
	out := newGatedWriter()
	sink := newConsoleSink(out, 8, 1<<20)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 8 {
			_, _ = fmt.Fprintf(sink, "line %d\n", i)
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("console writes blocked on a stalled output")
	}

	close(out.release)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	testutil.FailErr(t, "flush", sink.flush(ctx))

	got := out.String()
	for i := range 8 {
		if !strings.Contains(got, fmt.Sprintf("line %d\n", i)) {
			t.Fatalf("output missing line %d:\n%s", i, got)
		}
	}
	if strings.Contains(got, "dropped") {
		t.Fatalf("no drop expected within queue budget:\n%s", got)
	}
}

func TestConsoleSinkDropsBacklogAndReportsCountOnceDrained(t *testing.T) {
	out := newGatedWriter()
	sink := newConsoleSink(out, 4, 1<<20)

	// The head line is in flight and blocked; four more fill the queue;
	// everything after that is dropped.
	_, _ = fmt.Fprint(sink, "line 0\n")
	<-out.entered
	for i := 1; i < 12; i++ {
		_, _ = fmt.Fprintf(sink, "line %d\n", i)
	}
	if got := sink.dropped.Load(); got != 7 {
		t.Fatalf("dropped = %d want 7", got)
	}

	close(out.release)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	testutil.FailErr(t, "flush", sink.flush(ctx))

	got := out.String()
	if !strings.Contains(got, "console output dropped 7 lines while stderr was not draining\n") {
		t.Fatalf("drop notice missing:\n%s", got)
	}
	if !strings.HasPrefix(got, "line 0\n") {
		t.Fatalf("in-flight head line must precede the notice:\n%s", got)
	}
	if strings.Contains(got, "line 11\n") {
		t.Fatalf("dropped tail line must not appear:\n%s", got)
	}
	if sink.dropped.Load() != 0 {
		t.Fatalf("drop counter must reset after the notice, got %d", sink.dropped.Load())
	}
}

func TestConsoleSinkDropsWhenByteBudgetIsExhausted(t *testing.T) {
	out := newGatedWriter()
	sink := newConsoleSink(out, 64, 64)

	// 30 bytes each: the first is in flight and still counted, the second fits,
	// the third exceeds the 64-byte budget while the queue still has room.
	line := strings.Repeat("x", 29) + "\n"
	_, _ = sink.Write([]byte(line))
	<-out.entered
	for range 2 {
		_, _ = sink.Write([]byte(line))
	}
	if got := sink.dropped.Load(); got != 1 {
		t.Fatalf("dropped = %d want 1", got)
	}

	close(out.release)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	testutil.FailErr(t, "flush", sink.flush(ctx))
	if got := strings.Count(out.String(), line); got != 2 {
		t.Fatalf("delivered %d lines want 2:\n%s", got, out.String())
	}
}

func TestConsoleSinkPreservesOrder(t *testing.T) {
	var out bytes.Buffer
	sink := newConsoleSink(&out, 64, 1<<20)
	for i := range 20 {
		_, _ = fmt.Fprintf(sink, "%d\n", i)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	testutil.FailErr(t, "flush", sink.flush(ctx))

	var want strings.Builder
	for i := range 20 {
		fmt.Fprintf(&want, "%d\n", i)
	}
	if out.String() != want.String() {
		t.Fatalf("order changed:\n%s", out.String())
	}
}

func TestConsoleSinkFlushHonorsContextWhileStalled(t *testing.T) {
	out := newGatedWriter()
	sink := newConsoleSink(out, 4, 1<<20)
	_, _ = sink.Write([]byte("stuck\n"))

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := sink.flush(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("flush err = %v want deadline exceeded", err)
	}
	close(out.release)
}
