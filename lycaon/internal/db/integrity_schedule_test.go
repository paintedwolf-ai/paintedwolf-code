package db

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestAuditBudgetUsesAvailableBytesAndActualPageSize(t *testing.T) {
	for _, tc := range []struct {
		name   string
		free   uint64
		known  bool
		page   uint64
		budget uint64
	}{
		{"maximum", 8 << 30, true, 4096, auditMaximumBytes},
		{"free fraction", 512 << 20, true, 65536, 64 << 20},
		{"small volume", 128 << 20, true, 512, auditUnknownCapacityBytes},
		{"low space", 8 << 20, true, 4096, 1 << 20},
		{"unknown", 0, false, 4096, auditUnknownCapacityBytes},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := auditBudgetFrames(tc.free, tc.known, tc.page)
			if got != tc.budget/(tc.page+24) {
				t.Fatalf("frames = %d; budget = %d", got, tc.budget)
			}
		})
	}
}

func TestAuditRetryDoesNotRequireQuietOrCompleteCheckpoint(t *testing.T) {
	wakes := make(chan time.Time, 1)
	writes, attempts := 0, 0
	testutil.FailErr(t, "finish audit amid writes", auditAttempts(t.Context(), func(context.Context) error {
		attempts++
		if attempts == 4 {
			return nil
		}
		return errAuditDeferred
	}, func(ctx context.Context) error {
		writes++
		wakes <- time.Time{}
		return waitForAuditRetry(ctx, wakes, func() error {
			// An unrelated reader can retain frames. A successful passive checkpoint
			// still permits the next attempt; it need not empty the WAL.
			writes++
			return nil
		})
	}))
	if attempts != 4 || writes != 6 {
		t.Fatalf("attempts=%d writes=%d", attempts, writes)
	}
	for attempt, want := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, 30 * time.Second} {
		if got := auditRetryDelay(attempt + 1); got != want {
			t.Fatalf("attempt %d delay=%v want=%v", attempt+1, got, want)
		}
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := waitForAuditRetry(canceled, wakes, func() error { t.Fatal("checkpoint ran after shutdown"); return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled wait = %v", err)
	}
}

func TestAuditTurnPreservesWritesAndYieldsToReclamationPressure(t *testing.T) {
	_, store := uncleanAuditStore(t)
	release, err := store.audit.backgroundTurn(t.Context())
	testutil.FailErr(t, "acquire audit turn", err)
	checkCtx, finish := store.audit.begin(t.Context(), 1024)
	_, err = store.ExecContext(t.Context(), `INSERT INTO store_meta(key,value) VALUES('foreground-during-audit','saved')`)
	testutil.FailErr(t, "save while audit owns background turn", err)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := RunRetention(canceled, store, DefaultRetention()); !errors.Is(err, context.Canceled) {
		t.Fatalf("waiting maintenance cancellation = %v", err)
	}
	store.audit.mu.Lock()
	store.audit.maintenanceWaiting++
	store.audit.mu.Unlock()
	store.audit.resourceLimit(0)
	store.audit.mu.Lock()
	store.audit.maintenanceWaiting--
	store.audit.mu.Unlock()
	if !errors.Is(context.Cause(checkCtx), errAuditDeferred) {
		t.Fatalf("low-space audit cause = %v", context.Cause(checkCtx))
	}
	finish()
	release()
	_, err = RunRetention(t.Context(), store, DefaultRetention())
	testutil.FailErr(t, "maintenance takes released turn", err)
	testutil.FailErr(t, "audit completes after maintenance", AuditIntegrity(t.Context(), store))
	var value string
	testutil.FailErr(t, "read foreground save", store.QueryRowContext(t.Context(), `SELECT value FROM store_meta WHERE key='foreground-during-audit'`).Scan(&value))
	if value != "saved" {
		t.Fatal("audit coordination lost foreground write")
	}
}

func TestAuditWithZeroFrameBudgetCanFinishWithoutWrites(t *testing.T) {
	_, store := uncleanAuditStore(t)
	ctx, finish := store.audit.begin(t.Context(), 0)
	defer finish()
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := maintenanceTurn(canceled, store); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled reclamation request = %v", err)
	}
	store.audit.resourceLimit(0)
	store.audit.checkpoint(WALCheckpoint{})
	testutil.FailErr(t, "audit without retaining new WAL", auditSnapshot(ctx, store.reader, nil))
	store.audit.checkpoint(WALCheckpoint{LogFrames: 1})
	if !errors.Is(context.Cause(ctx), errAuditDeferred) {
		t.Fatalf("zero budget retained frames: %v", context.Cause(ctx))
	}
}

func TestAuditStopsWhenPressureObservationFails(t *testing.T) {
	path, store := uncleanAuditStore(t)
	ctx, finish := store.audit.begin(t.Context(), 1024)
	defer finish()
	testutil.FailErr(t, "close checkpoint writer", store.writer.Close())
	err := checkpointOnce(t.Context(), store, path)
	if err == nil || context.Cause(ctx) == nil {
		t.Fatalf("observation error=%v audit cause=%v", err, context.Cause(ctx))
	}
	if store.Failure() != nil {
		t.Fatalf("observation failure quarantined store: %v", store.Failure())
	}
}

func TestAuditCapacityFailureUsesUnknownBudget(t *testing.T) {
	_, store := uncleanAuditStore(t)
	// The open database remains usable while its capacity lookup fails.
	store.health.path = filepath.Join(t.TempDir(), "missing", "store.db")
	budget, err := auditFrameBudget(t.Context(), store)
	testutil.FailErr(t, "budget without filesystem statistics", err)
	var page uint64
	testutil.FailErr(t, "read page size", store.reader.QueryRow("PRAGMA page_size").Scan(&page))
	if budget != auditUnknownCapacityBytes/(page+24) {
		t.Fatalf("unknown capacity budget = %d", budget)
	}
	testutil.FailErr(t, "complete audit without filesystem statistics", AuditIntegrity(t.Context(), store))
}
