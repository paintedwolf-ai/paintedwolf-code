//go:build integration

package cadence

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	scanbase "github.com/lycaon/lycaon/internal/scan"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCadenceDispatchRequeuesWhenCallerDeadlineExpires(t *testing.T) {
	clock := newTestClock()
	cadence, store, _ := newBlockingCadence(t, clock)
	dir := t.TempDir()
	seedNonEmptyProject(t, dir)

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	err := cadence.BaselineRoot(ctx, dir)
	if err == nil {
		t.Fatalf("attach error = %v, want the deadline named", err)
	}
	canonical, err := scanbase.CanonicalPath(dir)
	testutil.FailErr(t, "CanonicalPath", err)
	rows, err := store.ListSeriesForPath(context.Background(), canonical)
	testutil.FailErr(t, "ListSeriesForPath", err)
	for _, row := range rows {
		if row.DispatchToken != "" || row.DueAt.IsZero() {
			t.Fatalf("scanner %s still holds a claim after the deadline: %+v", row.ScannerID, row)
		}
	}
	scans, err := store.ListByCanonicalPath(context.Background(), canonical)
	testutil.FailErr(t, "ListByCanonicalPath", err)
	if len(scans) != 0 {
		t.Fatalf("no scan may be enqueued without a generation, got %d", len(scans))
	}
}

func TestCadenceHeartbeatKeepsALiveClaimFromBeingRecovered(t *testing.T) {
	clock := newTestClock()
	cadence, store, coord := newBlockingCadence(t, clock)
	dir := t.TempDir()
	seedNonEmptyProject(t, dir)
	canonical, err := scanbase.CanonicalPath(dir)
	testutil.FailErr(t, "CanonicalPath", err)

	done := make(chan error, 1)
	go func() {
		done <- cadence.BaselineRoot(context.Background(), dir)
	}()
	waitFor(t, func() bool {
		coord.mu.Lock()
		defer coord.mu.Unlock()
		return coord.waited > 0
	})
	// Heartbeats retain the claim while publication waits beyond its TTL.
	clock.Advance(scanbase.DispatchClaimTTL + time.Minute)
	rows, err := store.ListSeriesForPath(context.Background(), canonical)
	testutil.FailErr(t, "ListSeriesForPath", err)
	for i := range rows {
		testutil.FailErr(t, "heartbeat", store.HeartbeatClaim(context.Background(), rows[i], clock.Now()))
	}
	cadence.Tick(context.Background())
	rows, err = store.ListSeriesForPath(context.Background(), canonical)
	testutil.FailErr(t, "ListSeriesForPath after tick", err)
	for _, row := range rows {
		if row.DispatchToken == "" {
			t.Fatalf("scanner %s lost its live claim to recovery", row.ScannerID)
		}
	}
	close(coord.release)
	testutil.FailErr(t, "attach once the generation arrived", <-done)
	rows, err = store.ListSeriesForPath(context.Background(), canonical)
	testutil.FailErr(t, "ListSeriesForPath after attach", err)
	for _, row := range rows {
		if row.LastCoveredSnapshotID == "" || row.DispatchToken != "" {
			t.Fatalf("scanner %s did not take the generation as its base: %+v", row.ScannerID, row)
		}
	}
}

func TestCadenceWritesDuringAClaimAccumulateWithoutBecomingDue(t *testing.T) {
	clock := newTestClock()
	cadence, store, coord := newBlockingCadence(t, clock)
	dir := t.TempDir()
	seedNonEmptyProject(t, dir)
	canonical, err := scanbase.CanonicalPath(dir)
	testutil.FailErr(t, "CanonicalPath", err)

	done := make(chan struct{})
	go func() {
		_ = cadence.BaselineRoot(context.Background(), dir)
		close(done)
	}()
	waitFor(t, func() bool {
		coord.mu.Lock()
		defer coord.mu.Unlock()
		return coord.waited > 0
	})
	testutil.FailErr(t, "NoteWrites during claim", noteCadenceWrites(context.Background(), cadence, dir, []string{"main.go"}))
	rows, err := store.ListSeriesForPath(context.Background(), canonical)
	testutil.FailErr(t, "ListSeriesForPath", err)
	for _, row := range rows {
		if !row.DueAt.IsZero() {
			t.Fatalf("scanner %s became due while claimed: %+v", row.ScannerID, row)
		}
		if len(row.DesiredPaths) != 1 || row.DesiredPaths[0] != "main.go" {
			t.Fatalf("scanner %s lost the write recorded while claimed: %+v", row.ScannerID, row)
		}
	}
	close(coord.release)
	<-done
	rows, err = store.ListSeriesForPath(context.Background(), canonical)
	testutil.FailErr(t, "ListSeriesForPath after settle", err)
	for _, row := range rows {
		if row.DispatchToken != "" || row.DueAt.IsZero() {
			t.Fatalf("scanner %s did not re-arm its held desire when the claim settled: %+v", row.ScannerID, row)
		}
	}
}

func TestCadenceIgnoredWritesNeverSettleOrArm(t *testing.T) {
	clock := newTestClock()
	cadence, store := newTestCadence(t, clock)
	dir := t.TempDir()
	seedSizedProject(t, dir, 40)
	testutil.FailErr(t, "gitignore", os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("scratch/\n"), 0o644))
	testutil.FailErr(t, "BaselineRoot", cadence.BaselineRoot(context.Background(), dir))
	completeCadenceScans(t, cadence, store)
	canonical, err := scanbase.CanonicalPath(dir)
	testutil.FailErr(t, "CanonicalPath", err)
	before, err := store.ListByCanonicalPath(context.Background(), canonical)
	testutil.FailErr(t, "list before", err)

	testutil.FailErr(t, "mkdir scratch", os.MkdirAll(filepath.Join(dir, "scratch", "deep"), 0o755))
	testutil.FailErr(t, "write scratch", os.WriteFile(filepath.Join(dir, "scratch", "deep", "out"), []byte("x"), 0o644))
	testutil.FailErr(t, "NoteWrites ignored", noteCadenceWrites(context.Background(), cadence, dir, []string{"scratch/deep/out"}))
	rows, err := store.ListSeriesForPath(context.Background(), canonical)
	testutil.FailErr(t, "ListSeriesForPath", err)
	for _, row := range rows {
		if !row.DueAt.IsZero() || len(row.DesiredPaths) != 0 {
			t.Fatalf("an ignored write armed scanner %s: %+v", row.ScannerID, row)
		}
	}
	clock.Advance(time.Minute)
	cadence.Tick(context.Background())
	after, err := store.ListByCanonicalPath(context.Background(), canonical)
	testutil.FailErr(t, "list after", err)
	if len(after) != len(before) {
		t.Fatalf("an ignored write scheduled a scan: %d -> %d", len(before), len(after))
	}
}

func TestCanceledDispatchReturnsClaimsWithoutWaitingForExpiry(t *testing.T) {
	clock := newTestClock()
	cadence, store, coord := newBlockingCadence(t, clock)
	dir := t.TempDir()
	seedNonEmptyProject(t, dir)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- cadence.BaselineRoot(ctx, dir) }()
	waitFor(t, func() bool { coord.mu.Lock(); defer coord.mu.Unlock(); return coord.waited > 0 })
	cancel()
	if err := <-done; err == nil {
		t.Fatal("canceled publication succeeded")
	}
	canonical, err := scanbase.CanonicalPath(dir)
	testutil.FailErr(t, "canonical root", err)
	rows, err := store.ListSeriesForPath(t.Context(), canonical)
	testutil.FailErr(t, "inspect canceled claims", err)
	for _, row := range rows {
		if row.DispatchToken != "" || row.DueAt.IsZero() {
			t.Fatalf("claim stranded until expiry: %+v", row)
		}
	}
}

func TestTerminalSettlementDoesNotPublishTheNextGeneration(t *testing.T) {
	clock := newTestClock()
	cadence, store := newTestCadence(t, clock)
	dir := t.TempDir()
	seedNonEmptyProject(t, dir)
	testutil.FailErr(t, "baseline", cadence.BaselineRoot(t.Context(), dir))
	_, err := cadence.RequestFull(t.Context(), dir, nil, api.ScanTriggerManual, scanbase.FullScanContext{})
	testutil.FailErr(t, "request scan", err)
	claimed, err := store.ClaimNext(t.Context())
	testutil.FailErr(t, "claim scan", err)
	won, err := store.MarkComplete(t.Context(), claimed, &scanoutput.Result{})
	testutil.FailErr(t, "complete scan", err)
	if !won {
		t.Fatal("completion lost")
	}
	completed, err := store.Get(t.Context(), claimed.ID)
	testutil.FailErr(t, "read completion", err)
	coord := &blockingCoordinator{CoordinatorImpl: cadence.Coordinator.(*scanbase.CoordinatorImpl), release: make(chan struct{})}
	cadence.Coordinator = coord
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	cadence.OnTerminal(ctx, *completed)
	if ctx.Err() != nil || coord.waited != 0 {
		t.Fatalf("terminal settlement started source preparation: error=%v calls=%d", ctx.Err(), coord.waited)
	}
	row, err := store.GetSeries(t.Context(), claimed.CanonicalPath, claimed.ScannerID)
	testutil.FailErr(t, "read settled series", err)
	if row.ActiveScanID != "" || row.LastCompletedAt.IsZero() {
		t.Fatalf("terminal facts were not settled: %+v", row)
	}
}
