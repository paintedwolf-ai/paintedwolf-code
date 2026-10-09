package api

import (
	"context"
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
	t.Cleanup(s.StopBackground)
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
	s.WaitForBackground(t.Context())
	if got := completed.Load(); got != 2000 {
		t.Fatalf("drained callbacks = %d, want 2000", got)
	}
}

func TestDetachedDrainIncludesNestedWorkAndHonorsCancellation(t *testing.T) {
	s := &Server{}
	t.Cleanup(s.StopBackground)
	childStarted := make(chan struct{})
	release := make(chan struct{})
	s.background.Go(t.Context(), func(ctx context.Context) {
		s.background.Go(ctx, func(context.Context) {
			close(childStarted)
			<-release
		})
	})
	<-childStarted
	drained := make(chan struct{})
	go func() { s.WaitForBackground(t.Context()); close(drained) }()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	s.WaitForBackground(ctx)
	select {
	case <-drained:
		t.Fatal("drain returned while nested detached work was active")
	default:
	}
	close(release)
	select {
	case <-drained:
	case <-time.After(5 * time.Second):
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
		WithTestAuth(req)

		resp, err := http.DefaultClient.Do(req)
		testutil.FailErr(t, "execute request", err)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("projects status = %d", resp.StatusCode)
		}
	})

	drainCtx, drainCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer drainCancel()
	if err := sourcecatalog.Process().Drain(drainCtx); err != nil {
		t.Fatalf("drain source catalog after server cleanup: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		runtime.GC()
		growth := runtime.NumGoroutine() - baselineGoroutines
		if growth <= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("goroutines did not settle after cleanup: baseline %d, now %d (growth %d)",
				baselineGoroutines, runtime.NumGoroutine(), growth)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
