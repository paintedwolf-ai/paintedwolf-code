package api

import (
	"context"
	"errors"
	"net/http"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestDetachedDrainOverlapsRepeatedAdmissions(t *testing.T) {
	s := &Server{}
	t.Cleanup(func() { s.StopBackground(t.Context()) })
	var completed atomic.Int64
	var callers sync.WaitGroup
	for range 8 {
		callers.Go(func() {
			for range 250 {
				s.background.Go(t.Context(), func(context.Context) { completed.Add(1) })
				s.background.Wait(t.Context())
			}
		})
	}
	callers.Wait()
	testutil.FailErr(t, "wait for background", s.WaitForBackground(t.Context()))
	if got := completed.Load(); got != 2000 {
		t.Fatalf("drained callbacks = %d, want 2000", got)
	}
}

func TestDetachedDrainIncludesNestedWorkAndHonorsCancellation(t *testing.T) {
	s := &Server{}
	t.Cleanup(func() { s.StopBackground(t.Context()) })
	childStarted := make(chan struct{})
	release := make(chan struct{})
	s.background.Go(t.Context(), func(ctx context.Context) {
		s.background.Go(ctx, func(context.Context) {
			close(childStarted)
			<-release
		})
	})
	<-childStarted
	drained := make(chan error, 1)
	go func() { drained <- s.WaitForBackground(t.Context()) }()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := s.WaitForBackground(ctx); err == nil {
		t.Fatal("canceled WaitForBackground returned nil")
	}
	select {
	case <-drained:
		t.Fatal("drain returned while nested detached work was active")
	default:
	}
	close(release)
	select {
	case err := <-drained:
		testutil.FailErr(t, "drain after nested work", err)
	case <-t.Context().Done():
		t.Fatal("drain did not return after nested work completed")
	}
}

func TestServerCleanupReliablyReleasesResourcesInvariant(t *testing.T) {
	runtime.GC()
	baselineGoroutines := runtime.NumGoroutine()

	t.Run("serve_and_cleanup", func(t *testing.T) {
		srv := newTestServer(t)
		baseURL := startTestHTTPServer(t, srv)

		projectRoot := t.TempDir()
		createProjectForTest(t, srv, projectRoot)

		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, baseURL+"/v1/projects", nil)
		testutil.FailErr(t, "build request", err)
		req.Header.Set("Authorization", "Bearer "+TestAPIToken)

		resp, err := http.DefaultClient.Do(req)
		testutil.FailErr(t, "execute request", err)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("projects status = %d", resp.StatusCode)
		}
	})

	drainCtx := t.Context()
	if err := sourcecatalog.Process().Drain(drainCtx); err != nil {
		t.Fatalf("drain source catalog after server cleanup: %v", err)
	}

	settleCtx := t.Context()
	for {
		runtime.GC()
		growth := runtime.NumGoroutine() - baselineGoroutines
		if growth <= 2 {
			break
		}
		if settleCtx.Err() != nil {
			t.Fatalf("goroutines did not settle after cleanup: baseline %d, now %d (growth %d)",
				baselineGoroutines, runtime.NumGoroutine(), growth)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

type stubEventOutbox struct {
	remaining atomic.Int64
	epoch     atomic.Uint64
	calls     atomic.Int64
	onWait    func(call int64)
}

func (o *stubEventOutbox) Wait(ctx context.Context) error {
	call := o.calls.Add(1)
	if o.onWait != nil {
		o.onWait(call)
	}
	if o.remaining.Load() < 0 || o.remaining.Add(-1) >= 0 {
		o.epoch.Add(1)
	}
	return ctx.Err()
}

func (o *stubEventOutbox) Epoch() uint64 {
	return o.epoch.Load()
}

func TestWaitForBackgroundLoopsOnOutboxActivityUntilFixedPoint(t *testing.T) {
	outbox := &stubEventOutbox{}
	// Exceed the former 16-pass cap to prove there is no silent pass limit.
	outbox.remaining.Store(20)
	s := &Server{eventOutbox: outbox}
	t.Cleanup(func() { s.StopBackground(t.Context()) })

	testutil.FailErr(t, "wait for background fixed point", s.WaitForBackground(t.Context()))
	if got := outbox.calls.Load(); got != 21 {
		t.Fatalf("outbox Wait calls = %d, want 21 (20 active passes + 1 quiet pass)", got)
	}
}

func TestWaitForBackgroundReturnsContextErrWhenWorkNeverSettles(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	outbox := &stubEventOutbox{
		onWait: func(call int64) {
			if call >= 20 {
				cancel()
			}
		},
	}
	outbox.remaining.Store(-1)
	s := &Server{eventOutbox: outbox}
	t.Cleanup(func() { s.StopBackground(t.Context()) })

	err := s.WaitForBackground(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("WaitForBackground err = %v, want context.Canceled", err)
	}
	if got := outbox.calls.Load(); got < 20 {
		t.Fatalf("outbox Wait calls = %d, want >= 20 before cancellation", got)
	}
}
