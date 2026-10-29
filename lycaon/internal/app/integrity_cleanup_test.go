package app

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestIntegrityCleanupGatePreservesForegroundAndResourceRunners(t *testing.T) {
	for _, name := range []string{"store-coupled-reconcile", "source-blob-gc", "content-blob-gc", "history-retention", "prompt-attachment-maintenance", "content-density", "worker-branch-retention"} {
		if !cleanupRequiresIntegrity(name) {
			t.Fatalf("irreversible runner %s bypasses pending audit", name)
		}
	}
	for _, name := range []string{"wal-checkpointer", "store-maintenance", "boot-recovery", "vault-unlock-sweep", "worker-poller", "scan-runner", "debug-retention"} {
		if cleanupRequiresIntegrity(name) {
			t.Fatalf("resource or ordinary runner %s waits on exhaustive verification", name)
		}
	}
}

func TestCleanupWaitCancellationPreservesPendingAuditAndOrdinaryWrites(t *testing.T) {
	store := testdbfixture.Open(t, "store.db")
	markCleanupAuditPending(t, store)
	called := false
	run := afterIntegrityAudit(store, func(context.Context) error { called = true; return nil })
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := run(ctx); !errors.Is(err, context.Canceled) || called {
		t.Fatalf("canceled wait ran cleanup: err=%v called=%v", err, called)
	}
	_, err := store.ExecContext(t.Context(), "INSERT INTO store_meta(key,value) VALUES('foreground-pending','saved')")
	testutil.FailErr(t, "save while cleanup is pending", err)
	pending, err := store.IntegrityAuditPending(t.Context())
	testutil.FailErr(t, "read persistent audit after canceled runner", err)
	if !pending {
		t.Fatal("canceled cleanup discharged background verification")
	}
	testutil.FailErr(t, "complete audit", db.AuditIntegrity(t.Context(), store))
	testutil.FailErr(t, "release audited cleanup runner", run(t.Context()))
	if !called {
		t.Fatal("audit success did not release cleanup")
	}
}

func TestAuditSuccessRetriesCustodyFinalizationWithoutAnotherAudit(t *testing.T) {
	store := testdbfixture.Open(t, "store.db")
	markCleanupAuditPending(t, store)
	refused := errors.New("recovery finalization unavailable")
	calls := 0
	err := runStoreIntegrityAudit(t.Context(), store, func() error { calls++; return refused })
	if !errors.Is(err, refused) || calls != 1 {
		t.Fatalf("finalization error=%v calls=%d", err, calls)
	}
	pending, err := store.IntegrityAuditPending(t.Context())
	testutil.FailErr(t, "read completed audit before retry", err)
	if pending {
		t.Fatal("cleanup failure discarded completed verification")
	}
	// A repeated audit would attempt another metadata write. Finalizer retry
	// needs only the completed obligation, even when the writer is unavailable.
	_, err = store.ExecContext(t.Context(), "PRAGMA query_only=ON")
	testutil.FailErr(t, "make a repeated audit observable", err)
	defer func() { _, _ = store.ExecContext(t.Context(), "PRAGMA query_only=OFF") }()
	err = runStoreIntegrityAudit(t.Context(), store, func() error { calls++; return refused })
	if !errors.Is(err, refused) || calls != 2 || store.Failure() != nil {
		t.Fatalf("finalization retry repeated the audit: err=%v calls=%d health=%v", err, calls, store.Failure())
	}
}

func TestAuditRunnerNeverFinalizesConfirmedDamage(t *testing.T) {
	store := testdbfixture.Open(t, "store.db")
	markCleanupAuditPending(t, store)
	_, err := store.ExecContext(t.Context(), "PRAGMA foreign_keys=OFF; INSERT INTO project_roots(id,project_id,path,label,added_at) VALUES('broken','missing','/missing','missing','2026-01-01T00:00:00.000000000Z'); PRAGMA foreign_keys=ON")
	testutil.FailErr(t, "seed confirmed relation damage", err)
	called := false
	err = runStoreIntegrityAudit(t.Context(), store, func() error { called = true; return nil })
	if !errors.Is(err, db.ErrStoreIncompatible) || called || store.Failure() == nil {
		t.Fatalf("damage authorized finalization: err=%v called=%v failure=%v", err, called, store.Failure())
	}
	err = afterIntegrityAudit(store, func(context.Context) error { called = true; return nil })(t.Context())
	if !errors.Is(err, db.ErrStoreIncompatible) || called {
		t.Fatalf("quarantine released cleanup: err=%v called=%v", err, called)
	}
}

func TestAuditRunnerParksAfterSuccessfulCustodyFinalization(t *testing.T) {
	store := testdbfixture.Open(t, "store.db")
	markCleanupAuditPending(t, store)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	calls := 0
	err := runStoreIntegrityAudit(ctx, store, func() error { calls++; cancel(); return nil })
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("completed runner lifecycle: calls=%d err=%v", calls, err)
	}
}

func markCleanupAuditPending(t *testing.T, store *db.Store) {
	t.Helper()
	_, err := store.ExecContext(t.Context(), `INSERT INTO store_meta(key,value) VALUES('integrity_audit_failed','integrity audit pending') ON CONFLICT(key) DO UPDATE SET value=excluded.value`)
	testutil.FailErr(t, "seed persistent background audit obligation", err)
}
