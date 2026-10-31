package commandprogram

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
)

type closeEOFSource struct {
	entered, closed chan struct{}
	enter, close    sync.Once
}

func (s *closeEOFSource) Read([]byte) (int, error) {
	s.enter.Do(func() { close(s.entered) })
	<-s.closed
	return 0, io.EOF
}
func (s *closeEOFSource) Close() error { s.close.Do(func() { close(s.closed) }); return nil }
func lifecyclePipe(t *testing.T, ctx context.Context, source io.ReadCloser) *ScreenedPipe {
	t.Helper()
	budget, err := NewByteBudget("fixture", 1<<20)
	testutil.FailErr(t, "admit stream", err)
	screen := projectorScreen{projector: captureprojection.New(secretmatch.NewInertMatcher(), nil), options: captureprojection.StreamOptions{WorkingMemory: &streamSharedMemoryFixture{maximum: 32 << 20}, Bounds: secretmatch.StreamBounds{InputBytes: 1 << 20, OutputBytes: 1 << 20, MemoryBytes: 32 << 20, DetectorWork: 1 << 20}}}
	p, err := NewScreenedPipe(ctx, screen, source, budget)
	testutil.FailErr(t, "open screen", err)
	t.Cleanup(func() { _ = p.Close() })
	return p
}
func TestScreenedPipeCloseInducedEOFCannotComplete(t *testing.T) {
	for _, external := range []bool{false, true} {
		t.Run(map[bool]string{false: "consumer", true: "parent"}[external], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			parent, stop := context.WithCancelCause(ctx)
			defer stop(nil)
			source := &closeEOFSource{entered: make(chan struct{}), closed: make(chan struct{})}
			p := lifecyclePipe(t, parent, source)
			select {
			case <-source.entered:
			case <-ctx.Done():
				t.Fatal("source never reached read")
			}
			want := ErrConsumerClosed
			if external {
				want = errors.New("synthetic parent cancellation")
				stop(want)
			} else {
				testutil.FailErr(t, "close consumer", p.Close())
			}
			metadata, err := p.Wait(ctx)
			wantTermination := StreamConsumerClosed
			if external {
				wantTermination = StreamCanceled
			}
			if got := p.pipe.Completion().Termination; got != wantTermination {
				t.Fatalf("wrong terminal classification %s want%s", got, wantTermination)
			}
			if !errors.Is(err, want) || metadata.Complete {
				t.Fatalf("close-induced EOF complete=%v err=%v", metadata.Complete, err)
			}
		})
	}
}
func TestScreenedPipeNaturalCompletionSurvivesLaterClose(t *testing.T) {
	p := lifecyclePipe(t, t.Context(), io.NopCloser(bytes.NewReader(nil)))
	metadata, err := p.Wait(t.Context())
	testutil.FailErr(t, "settle natural EOF", err)
	testutil.FailErr(t, "close completed screen", p.Close())
	after, err := p.Wait(t.Context())
	testutil.FailErr(t, "retain actual completion", err)
	if !metadata.Complete || !after.Complete {
		t.Fatal("later close rewrote successful screen")
	}
}

type lifecycleFailureSource struct{ cause error }

func (s lifecycleFailureSource) Read([]byte) (int, error) { return 0, s.cause }
func (s lifecycleFailureSource) Close() error             { return nil }
func TestScreenedPipePreservesActualSourceFailureAfterClose(t *testing.T) {
	cause := errors.New("synthetic utility output failure")
	p := lifecyclePipe(t, t.Context(), lifecycleFailureSource{cause: cause})
	before, err := p.Wait(t.Context())
	if !errors.Is(err, cause) || before.Complete {
		t.Fatalf("actual source failure lost %v", err)
	}
	testutil.FailErr(t, "close failed source", p.Close())
	after, err := p.Wait(t.Context())
	if !errors.Is(err, cause) || after.Complete {
		t.Fatalf("consumer close rewrote utility failure %v", err)
	}
}

type closeFailureSource struct {
	io.Reader
	cause error
}

func (s closeFailureSource) Close() error { return s.cause }
func TestScreenedPipeCloseFailureWithholdsCompletion(t *testing.T) {
	cause := errors.New("synthetic source close failure")
	p := lifecyclePipe(t, t.Context(), closeFailureSource{Reader: bytes.NewReader([]byte("kept")), cause: cause})
	got, readErr := io.ReadAll(p)
	facts, waitErr := p.Wait(t.Context())
	if string(got) != "kept" || !errors.Is(readErr, cause) || !errors.Is(waitErr, cause) || facts.Complete || p.pipe.Completion().Termination != StreamScreeningFailed {
		t.Fatalf("bytes=%q read=%v wait=%v facts=%+v completion=%+v", got, readErr, waitErr, facts, p.pipe.Completion())
	}
}

// Cancellation may make a blocked source report EOF; a failing close must
// preserve both causes while withholding the expected-consumer exception.
type canceledCloseFailureSource struct {
	*closeEOFSource
	cause error
}

func (s canceledCloseFailureSource) Close() error { _ = s.closeEOFSource.Close(); return s.cause }
func TestScreenedPipeCanceledSourceCloseKeepsBothCauses(t *testing.T) {
	parent, stop := context.WithCancelCause(t.Context())
	defer stop(nil)
	canceled, closed := errors.New("parent stopped"), errors.New("source close failed")
	source := canceledCloseFailureSource{closeEOFSource: &closeEOFSource{entered: make(chan struct{}), closed: make(chan struct{})}, cause: closed}
	pipe := lifecyclePipe(t, parent, source)
	<-source.entered
	stop(canceled)
	facts, err := pipe.Wait(t.Context())
	termination := pipe.pipe.Completion().Termination
	if !errors.Is(err, canceled) || !errors.Is(err, closed) || facts.Complete || (termination != StreamCanceled && termination != StreamScreeningFailed) {
		t.Fatalf("facts=%+v error=%v completion=%+v", facts, err, pipe.pipe.Completion())
	}
}
