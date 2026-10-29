package commandprogram

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"math"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func fixturePipe(t *testing.T, ctx context.Context, maximum int64) *BytePipe {
	t.Helper()
	budget, err := NewByteBudget("stream_bytes", maximum)
	testutil.FailErr(t, "create ingress budget", err)
	p, err := NewBytePipe(ctx, budget)
	testutil.FailErr(t, "create byte pipe", err)
	t.Cleanup(func() { _ = p.CloseConsumer() })
	return p
}

func TestBytePipePreservesBinaryBytesAndCompleteDigest(t *testing.T) {
	want := bytes.Repeat([]byte{'a', 0, '\r', '\n', 0xff}, 8000)
	p := fixturePipe(t, t.Context(), int64(len(want)))
	finished := make(chan error, 1)
	go func() {
		_, err := p.Write(want)
		if err == nil {
			err = p.CloseProducer(nil)
		}
		finished <- err
	}()
	got, err := io.ReadAll(p)
	testutil.FailErr(t, "read complete stream", err)
	testutil.FailErr(t, "settle producer", <-finished)
	sum := sha256.Sum256(want)
	c := p.Completion()
	if !bytes.Equal(got, want) || c.Termination != StreamEOF || c.EmittedBytes != int64(len(want)) || c.EmittedSHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("stream bytes or settlement differ: received=%d completion=%+v", len(got), c)
	}
}

func TestBytePipeBackpressureAndDeliberateConsumerStop(t *testing.T) {
	p := fixturePipe(t, t.Context(), 100)
	started, finished := make(chan struct{}), make(chan error, 1)
	go func() { close(started); _, err := p.Write([]byte("complete input")); finished <- err }()
	<-started
	select {
	case err := <-finished:
		t.Fatalf("unread pipe did not apply backpressure: %v", err)
	default:
	}
	got := make([]byte, 3)
	_, err := io.ReadFull(p, got)
	testutil.FailErr(t, "read consumer prefix", err)
	testutil.FailErr(t, "close early consumer", p.CloseConsumer())
	select {
	case err := <-finished:
		if !errors.Is(err, ErrConsumerClosed) {
			t.Fatalf("early close lost typed cause: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("early consumer close leaked producer")
	}
	if string(got) != "com" || p.Completion().Termination != StreamConsumerClosed {
		t.Fatalf("consumer stop=%+v", p.Completion())
	}
}

func TestBytePipeCancellationUnblocksBothDirections(t *testing.T) {
	for _, writing := range []bool{true, false} {
		t.Run(map[bool]string{true: "writer", false: "reader"}[writing], func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			p := fixturePipe(t, ctx, 100)
			finished := make(chan error, 1)
			go func() {
				if writing {
					_, err := p.Write([]byte("blocked"))
					finished <- err
				} else {
					_, err := p.Read(make([]byte, 1))
					finished <- err
				}
			}()
			cancel()
			select {
			case err := <-finished:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation cause=%v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("cancellation leaked stream operation")
			}
			if p.Completion().Termination != StreamCanceled {
				t.Fatalf("canceled stream=%+v", p.Completion())
			}
		})
	}
}

func TestBytePipeBudgetAndProducerFailureDoNotBecomeEOF(t *testing.T) {
	p := fixturePipe(t, t.Context(), 3)
	if n, err := p.Write([]byte("too much")); n != 0 || err == nil {
		t.Fatalf("over-budget bytes entered stream: %d %v", n, err)
	}
	if c := p.Completion(); c.Termination != StreamBudgetExceeded || c.EmittedBytes != 0 {
		t.Fatalf("over-budget settlement=%+v", c)
	}
	failed := fixturePipe(t, t.Context(), 10)
	cause := errors.New("synthetic producer read failed")
	testutil.FailErr(t, "close failed producer", failed.CloseProducer(cause))
	if _, err := failed.Read(make([]byte, 1)); !errors.Is(err, cause) {
		t.Fatalf("producer error concealed: %v", err)
	}
	if failed.Completion().Termination != StreamIncomplete {
		t.Fatalf("failure became EOF: %+v", failed.Completion())
	}
}

func TestByteBudgetOverflowCannotWrapAdmission(t *testing.T) {
	b, err := NewByteBudget("stream_bytes", math.MaxInt64)
	testutil.FailErr(t, "create large budget", err)
	testutil.FailErr(t, "charge admitted bytes", b.Charge(math.MaxInt64-1))
	var limit *StreamLimitError
	if err := b.Charge(2); !errors.As(err, &limit) || !limit.Overflow || limit.Observed != math.MaxInt64 || b.Used() != math.MaxInt64-1 {
		t.Fatalf("overflow changed budget: %+v %v", limit, err)
	}
	if err := b.Charge(-1); err == nil || b.Used() != math.MaxInt64-1 {
		t.Fatal("negative charge changed budget")
	}
}
