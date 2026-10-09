package webresearch

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestLoadFetchURLLimitsDefaultsFromBundledYAML(t *testing.T) {
	limits, err := LoadFetchURLLimits()
	testutil.FailErr(t, "load limits", err)
	if limits.SessionMaxFetches != 128 {
		t.Fatalf("session_max_fetches=%d want 128", limits.SessionMaxFetches)
	}
	if limits.SessionWindow != 30*time.Minute {
		t.Fatalf("session_window=%v want 30m", limits.SessionWindow)
	}
	if limits.HostMaxInFlight != 4 {
		t.Fatalf("host_max_in_flight=%d want 4", limits.HostMaxInFlight)
	}
	if limits.HostMinInterval != 0 {
		t.Fatalf("host_min_interval=%v want 0", limits.HostMinInterval)
	}
}

func TestFetchBudgetUnderCapPasses(t *testing.T) {
	b := NewFetchBudget(FetchURLLimits{
		SessionMaxFetches: 64,
		SessionWindow:     time.Minute,
		HostMaxInFlight:   4,
	})
	ctx := context.Background()
	for i := 0; i < 15; i++ {
		rel, err := b.Acquire(ctx, "sess-a", "https://example.com/page")
		testutil.FailErr(t, "acquire", err)
		rel()
	}
}

func TestFetchBudgetSessionExceededRejects(t *testing.T) {
	b := NewFetchBudget(FetchURLLimits{
		SessionMaxFetches: 3,
		SessionWindow:     time.Hour,
		HostMaxInFlight:   8,
	})
	ctx := context.Background()
	var releases []func()
	for i := 0; i < 3; i++ {
		rel, err := b.Acquire(ctx, "sess-b", "https://example.com/x")
		testutil.FailErr(t, "acquire under cap", err)
		releases = append(releases, rel)
	}
	_, err := b.Acquire(ctx, "sess-b", "https://example.com/y")
	if err == nil {
		t.Fatal("expected session budget reject")
	}
	rej := &toolrejection.ToolReject{}
	ok := errors.As(err, &rej)
	if !ok || rej.Code != "FETCH_URL_BUDGET_EXCEEDED" {
		t.Fatalf("got %#v want FETCH_URL_BUDGET_EXCEEDED", err)
	}
	if kind, _ := rej.Data["kind"].(string); kind != "session" {
		t.Fatalf("kind=%v want session", rej.Data["kind"])
	}
	if rej.Data["fetch_budget_used"] != 3 {
		t.Fatalf("used reservations = %v", rej.Data)
	}
	retry, ok := rej.Data["fetch_budget_retry_after_seconds"].(int)
	if !ok || retry <= 0 || retry > 3600 {
		t.Fatalf("invalid session-window recovery: %v", rej.Data)
	}

	for _, rel := range releases {
		rel()
	}
	// Other sessions remain independent.
	rel, err := b.Acquire(ctx, "sess-c", "https://example.com/z")
	testutil.FailErr(t, "other session", err)
	rel()
}

func TestFetchBudgetHostInFlightBlocks(t *testing.T) {
	b := NewFetchBudget(FetchURLLimits{
		SessionMaxFetches: 64,
		SessionWindow:     time.Hour,
		HostMaxInFlight:   4,
	})
	ctx := context.Background()
	var releases []func()
	for i := 0; i < 4; i++ {
		rel, err := b.Acquire(ctx, "sess-h", "https://same.example/p")
		testutil.FailErr(t, "acquire host slot", err)
		releases = append(releases, rel)
	}
	blocked := make(chan error, 1)
	go func() {
		short, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
		defer cancel()
		_, err := b.Acquire(short, "sess-h", "https://same.example/next")
		blocked <- err
	}()
	err := <-blocked
	if err == nil {
		t.Fatal("5th in-flight acquire should wait and cancel")
	}
	for _, rel := range releases {
		rel()
	}
	rel, err := b.Acquire(ctx, "sess-h", "https://same.example/after")
	testutil.FailErr(t, "after release", err)
	rel()
}

func TestFetchBudgetFourParallelSameHost(t *testing.T) {
	b := NewFetchBudget(FetchURLLimits{
		SessionMaxFetches: 64,
		SessionWindow:     time.Hour,
		HostMaxInFlight:   4,
	})
	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	releases := make(chan func(), 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rel, err := b.Acquire(ctx, "sess-p", "https://para.example/item")
			if err != nil {
				errs <- err
				return
			}
			releases <- rel
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("parallel acquire: %v", err)
	}
	close(releases)
	for rel := range releases {
		rel()
	}
}

func TestFetchBudgetRefundsSessionOnHostCancel(t *testing.T) {
	b := NewFetchBudget(FetchURLLimits{
		SessionMaxFetches: 2,
		SessionWindow:     time.Hour,
		HostMaxInFlight:   1,
	})
	ctx := context.Background()
	hold, err := b.Acquire(ctx, "sess-r", "https://hold.example/")
	testutil.FailErr(t, "hold slot", err)

	short, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = b.Acquire(short, "sess-r", "https://hold.example/2")
	if err == nil {
		t.Fatal("expected cancel while host saturated")
	}
	hold()

	// Host-wait cancel refunded its session reservation — one slot remains
	// (the successful hold still counts until the window ages).
	rel, err := b.Acquire(ctx, "sess-r", "https://hold.example/after")
	testutil.FailErr(t, "after refund", err)
	rel()
	_, err = b.Acquire(ctx, "sess-r", "https://hold.example/over")
	rej := &toolrejection.ToolReject{}
	ok := errors.As(err, &rej)
	if !ok || rej.Code != "FETCH_URL_BUDGET_EXCEEDED" {
		t.Fatalf("got %#v want session budget exceed", err)
	}
}

func TestFetchURLToolHonorsSessionBudget(t *testing.T) {
	t.Setenv(configdir.EnvConfigDir, t.TempDir())
	allowLoopbackFetch(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("ok body"))
	}))
	t.Cleanup(srv.Close)

	budget := NewFetchBudget(FetchURLLimits{
		SessionMaxFetches: 1,
		SessionWindow:     time.Hour,
		HostMaxInFlight:   4,
	})
	tctx := tools.ToolContext{SessionID: "budget-sess"}
	_, _, err := fetchURLTool(context.Background(), fetchURLToolArgs{
		URL:    srv.URL + "/a",
		Budget: budget,
		Tctx:   tctx,
	})
	testutil.FailErr(t, "first fetch", err)

	_, _, err = fetchURLTool(context.Background(), fetchURLToolArgs{
		URL:    srv.URL + "/b",
		Budget: budget,
		Tctx:   tctx,
	})
	rej := &toolrejection.ToolReject{}
	ok := errors.As(err, &rej)
	if !ok || rej.Code != "FETCH_URL_BUDGET_EXCEEDED" {
		t.Fatalf("second fetch got %#v want FETCH_URL_BUDGET_EXCEEDED", err)
	}
}

func TestFetchURLToolCacheHitSkipsBudget(t *testing.T) {
	t.Setenv(configdir.EnvConfigDir, t.TempDir())
	allowLoopbackFetch(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("cached once"))
	}))
	t.Cleanup(srv.Close)

	budget := NewFetchBudget(FetchURLLimits{
		SessionMaxFetches: 1,
		SessionWindow:     time.Hour,
		HostMaxInFlight:   4,
	})
	tctx := tools.ToolContext{SessionID: "cache-sess"}
	url := srv.URL + "/once"
	_, _, err := fetchURLTool(context.Background(), fetchURLToolArgs{
		URL: url, Budget: budget, Tctx: tctx,
	})
	testutil.FailErr(t, "network fetch", err)

	// Same URL from cache must not consume the (already spent) session budget.
	_, _, err = fetchURLTool(context.Background(), fetchURLToolArgs{
		URL: url, Budget: budget, Tctx: tctx,
	})
	testutil.FailErr(t, "cache hit", err)
}

func TestFetchBudgetRefundKeepsOtherReservationExpiry(t *testing.T) {
	b := NewFetchBudget(FetchURLLimits{SessionMaxFetches: 1, SessionWindow: time.Hour, HostMaxInFlight: 1})
	expired, err := b.reserveSession("session")
	testutil.FailErr(t, "reserve older waiting fetch", err)
	// The waiting fetch ages out before another fetch starts. Its later cancellation
	// must not refund the newer fetch or move the remaining budget's expiry.
	expired.started = time.Now().Add(-2 * time.Hour)
	current, err := b.reserveSession("session")
	testutil.FailErr(t, "reserve after expiry", err)
	b.refundSession("session", expired)
	_, err = b.reserveSession("session")
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "FETCH_URL_BUDGET_EXCEEDED" {
		t.Fatalf("stale refund released another fetch: %v", err)
	}
	if len(b.sessions["session"].reservations) != 1 || b.sessions["session"].reservations[0] != current {
		t.Fatal("refund changed a different reservation")
	}
}

func TestFetchBudgetReleaseCannotReleaseAnotherCall(t *testing.T) {
	b := NewFetchBudget(FetchURLLimits{SessionMaxFetches: 8, SessionWindow: time.Hour, HostMaxInFlight: 1})
	first, err := b.Acquire(t.Context(), "session", "https://example.test/")
	testutil.FailErr(t, "first fetch", err)
	first()
	second, err := b.Acquire(t.Context(), "session", "https://example.test/")
	testutil.FailErr(t, "second fetch", err)
	defer second()
	first()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	_, err = b.Acquire(ctx, "session", "https://example.test/")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("duplicate release freed a different fetch: %v", err)
	}
}

func TestFetchBudgetConcurrentStartsRespectHostInterval(t *testing.T) {
	const gap = 40 * time.Millisecond
	b := NewFetchBudget(FetchURLLimits{SessionMaxFetches: 8, SessionWindow: time.Hour, HostMaxInFlight: 4, HostMinInterval: gap})
	first, err := b.Acquire(t.Context(), "session", "https://example.test/")
	testutil.FailErr(t, "initial fetch", err)
	first()
	b.mu.Lock()
	started := b.hosts["example.test"].lastStart
	b.mu.Unlock()
	results := make(chan error, 3)
	for i := 0; i < 3; i++ {
		go func() {
			release, err := b.Acquire(t.Context(), "session", "https://example.test/")
			if err == nil {
				release()
			}
			results <- err
		}()
	}
	for i := 0; i < 3; i++ {
		testutil.FailErr(t, "spaced fetch", <-results)
	}
	b.mu.Lock()
	elapsed := b.hosts["example.test"].lastStart.Sub(started)
	b.mu.Unlock()
	if elapsed < 3*gap {
		t.Fatalf("concurrent starts bypassed host interval: %v", elapsed)
	}
}

func TestFetchBudgetCancellationDuringIntervalRefundsSlotAndReservation(t *testing.T) {
	b := NewFetchBudget(FetchURLLimits{SessionMaxFetches: 1, SessionWindow: time.Hour, HostMaxInFlight: 1, HostMinInterval: time.Hour})
	gate := b.hostGate("example.test")
	gate.lastStart = time.Now()
	previous := gate.lastStart
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	_, err := b.Acquire(ctx, "session", "https://example.test/")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting fetch cancellation: %v", err)
	}
	if len(gate.sem) != 0 || len(b.sessions["session"].reservations) != 0 || gate.lastStart != previous {
		t.Fatal("canceled wait consumed an in-flight slot, session reservation, or host start")
	}
}
