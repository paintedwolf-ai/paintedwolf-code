package execution

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	scanbase "github.com/lycaon/lycaon/internal/scan"
	scancfg "github.com/lycaon/lycaon/internal/scan/configuration"
	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/sourcesnapshot"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestWarmingScansAreListableButNotClaimable(t *testing.T) {
	database := testdbfixture.Open(t, "store.db")
	sqlStore := scanbase.NewSQLStore(database)
	cacheDir := t.TempDir()
	snapshots := sourcesnapshot.New(database, sourceblob.New(filepath.Join(cacheDir, "content")), filepath.Join(cacheDir, "observations.db"), backgroundwork.New(map[backgroundwork.Resource]backgroundwork.Limits{
		backgroundwork.ResourceMetadata: {Total: 1},
		backgroundwork.ResourceIO:       {Total: 1},
	}))
	t.Cleanup(func() { _ = snapshots.Close() })
	root := t.TempDir()
	testutil.FailErr(t, "write source", os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o644))
	canonical, err := scanbase.CanonicalPath(root)
	testutil.FailErr(t, "canonical", err)

	coord := scanbase.NewCoordinator(sqlStore, nil, snapshots)

	rec := api.CodeScan{
		ID: "11111111-1111-1111-1111-111111111111", CanonicalPath: canonical,
		Categories: []api.ScanCategory{api.ScanCategorySecurity}, ScannerID: "lycaon-sast",
		Status: api.CodeScanStatusPending, CreatedAt: time.Now().UTC(),
		SourceSnapshotID: api.SourceSnapshotWarming, Trigger: api.ScanTriggerManual,
	}
	testutil.FailErr(t, "insert warming", sqlStore.Insert(t.Context(), rec, nil, ""))
	if _, err := sqlStore.ClaimNext(t.Context()); !errors.Is(err, scanbase.ErrNoPendingScans) {
		t.Fatalf("ClaimNext = %v, want ErrNoPendingScans while warming", err)
	}
	listed, err := coord.List(t.Context(), []string{canonical}, 10)
	testutil.FailErr(t, "list warming", err)
	if len(listed) != 1 || listed[0].SourceSnapshotID != api.SourceSnapshotWarming {
		t.Fatalf("list = %+v", listed)
	}

	snapshot, err := snapshots.EnsurePath(t.Context(), canonical, sourcesnapshot.VerifyStat)
	testutil.FailErr(t, "publish", err)
	bound, err := sqlStore.BindPublishedSourceSnapshot(t.Context(), rec.ID, snapshot)
	testutil.FailErr(t, "bind", err)
	if bound.SourceSnapshotID != snapshot.ID {
		t.Fatalf("bound = %+v", bound)
	}
	claimed, err := sqlStore.ClaimNext(t.Context())
	testutil.FailErr(t, "claim after bind", err)
	if claimed.ID != rec.ID {
		t.Fatalf("claimed = %+v", claimed)
	}
}

// Publication retries keep warming rows pending until the retry limit.
func TestWarmingPublishFailureHoldsThenFails(t *testing.T) {
	database := testdbfixture.Open(t, "store.db")
	store := scanbase.NewSQLStore(database)
	root := t.TempDir()
	testutil.FailErr(t, "write source", os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o644))
	// A file at the cache directory path forces publication failure.
	blocked := filepath.Join(t.TempDir(), "blocked")
	testutil.FailErr(t, "block observation dir", os.WriteFile(blocked, []byte("blocked"), 0o600))
	cacheDir := t.TempDir()
	snapshots := sourcesnapshot.New(database, sourceblob.New(filepath.Join(cacheDir, "content")), filepath.Join(blocked, "observations.db"), backgroundwork.New(map[backgroundwork.Resource]backgroundwork.Limits{
		backgroundwork.ResourceMetadata: {Total: 1},
		backgroundwork.ResourceIO:       {Total: 1},
	}))
	t.Cleanup(func() { _ = snapshots.Close() })
	coord := scanbase.NewCoordinator(store, nil, snapshots)
	rec := api.CodeScan{
		ID: "22222222-2222-2222-2222-222222222222", CanonicalPath: root,
		Categories: []api.ScanCategory{api.ScanCategorySecurity}, Status: api.CodeScanStatusPending,
		CreatedAt: time.Now().UTC(), SourceSnapshotID: api.SourceSnapshotWarming,
		Trigger: api.ScanTriggerManual,
	}
	testutil.FailErr(t, "insert warming", store.Insert(t.Context(), rec, nil, ""))
	runner := NewRunner(store, &scanbase.MockRegistry{Scanner: &scanbase.MockScanner{}}, scanbase.NoopIngester{}, scancfg.DefaultRunnerConfig(), nil)
	runner.Coordinator = coord
	runner.RetryDelay = time.Nanosecond
	terminal := make(chan api.CodeScan, 1)
	runner.OnTerminal = func(_ context.Context, scan api.CodeScan) { terminal <- scan }

	for attempt := 1; attempt < maxPublishAttempts; attempt++ {
		testutil.FailErr(t, "service scan work", runner.serviceWork(t.Context()))
		held, err := store.Get(t.Context(), rec.ID)
		testutil.FailErr(t, "load held scan", err)
		if held == nil || held.Status != api.CodeScanStatusPending {
			t.Fatalf("attempt %d: scan = %+v, want pending hold", attempt, held)
		}
		select {
		case notified := <-terminal:
			t.Fatalf("attempt %d: unexpected terminal notification %+v", attempt, notified)
		default:
		}
	}

	testutil.FailErr(t, "service scan work", runner.serviceWork(t.Context()))
	failed, err := store.Get(t.Context(), rec.ID)
	testutil.FailErr(t, "load failed scan", err)
	if failed == nil || failed.Status != api.CodeScanStatusFailed {
		t.Fatalf("scan = %+v", failed)
	}
	select {
	case notified := <-terminal:
		if notified.ID != rec.ID || notified.Status != api.CodeScanStatusFailed {
			t.Fatalf("terminal notification = %+v", notified)
		}
	default:
		t.Fatal("failure did not notify waiters")
	}
}
