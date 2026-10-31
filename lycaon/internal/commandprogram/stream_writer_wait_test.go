package commandprogram

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestConcurrentPipeWritersExposeSerializationWait(t *testing.T) {
	_, root, observed := newWaitActivities(t)
	first, endFirst := root.Fork()
	defer endFirst()
	second, endSecond := root.Fork()
	defer endSecond()
	budget, err := NewByteBudget("stream_bytes", 100)
	testutil.FailErr(t, "pipe budget", err)
	pipe, err := NewBytePipe(t.Context(), budget)
	testutil.FailErr(t, "pipe", err)
	type result struct {
		n   int
		err error
	}
	a, b := make(chan result, 1), make(chan result, 1)
	go func() { n, err := pipe.write([]byte("ab"), first); a <- result{n, err} }()
	awaitPipeState(t, pipe.stream, func(p *pipeRendezvous) bool { return len(p.writers) == 1 })
	go func() { n, err := pipe.write([]byte("cde"), second); b <- result{n, err} }()
	awaitPipeState(t, pipe.stream, func(p *pipeRendezvous) bool { return len(p.writerQueue) == 1 })
	sleeping := blockActivity(t, root, WaitState{Until: root.Now().Add(time.Minute), Statement: "sleep 60"})
	observed.mu.Lock()
	active := observed.active
	observed.mu.Unlock()
	if !active {
		t.Fatal("serialized writer remained runnable")
	}
	sleeping()
	got := make([]byte, 3)
	_, err = io.ReadFull(pipe.ConsumerEnd(), got)
	testutil.FailErr(t, "read across writers", err)
	if string(got) != "abc" {
		t.Fatalf("bytes %q", got)
	}
	if first := <-a; first.n != 2 || first.err != nil {
		t.Fatalf("first writer %+v", first)
	}
	testutil.FailErr(t, "close consumer", pipe.CloseConsumer())
	if second := <-b; second.n != 1 || !errors.Is(second.err, ErrConsumerClosed) {
		t.Fatalf("partial second writer %+v", second)
	}
	completion := pipe.Completion()
	if completion.InputBytes != 5 || completion.EmittedBytes != 3 {
		t.Fatalf("completion %+v", completion)
	}
}

func TestCanceledPipeReadDoesNotPublishAWait(t *testing.T) {
	_, root, observed := newWaitActivities(t)
	restore := root.Deadline(root.Now().Add(time.Minute), "read -t 60")
	defer restore()
	budget, err := NewByteBudget("stream_bytes", 100)
	testutil.FailErr(t, "pipe budget", err)
	pipe, err := NewBytePipe(t.Context(), budget)
	testutil.FailErr(t, "pipe", err)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = pipe.read(ctx, make([]byte, 1), root)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("read %v", err)
	}
	observed.mu.Lock()
	defer observed.mu.Unlock()
	if len(observed.states) != 0 {
		t.Fatalf("canceled read published %+v", observed.states)
	}
}
