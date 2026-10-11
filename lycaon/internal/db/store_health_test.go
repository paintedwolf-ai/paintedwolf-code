package db

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func uncleanAuditStore(t *testing.T) (string, *Store) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "store.db")
	store, err := Open(path)
	testutil.FailErr(t, "create store", err)
	testutil.FailErr(t, "close store", store.Close())
	writer, err := openWriter(t.Context(), path, StoreOptions{})
	testutil.FailErr(t, "open crash fixture", err)
	testutil.FailErr(t, "record interrupted shutdown", markShutdownState(t.Context(), writer, false))
	testutil.FailErr(t, "close crash fixture", writer.Close())
	store, err = Open(path)
	testutil.FailErr(t, "open for background audit", err)
	t.Cleanup(func() { _ = store.Close() })
	return path, store
}

func injectIntegrityViolation(t *testing.T, store *Store) {
	t.Helper()
	_, err := store.writer.Exec(`PRAGMA foreign_keys=OFF`)
	testutil.FailErr(t, "disable fixture foreign keys", err)
	_, err = store.writer.Exec(`INSERT INTO project_roots(id, project_id, path, label, added_at) VALUES('broken','missing','/missing','missing','2026-01-01T00:00:00Z')`)
	testutil.FailErr(t, "inject integrity violation", err)
	_, err = store.writer.Exec(`PRAGMA foreign_keys=ON`)
	testutil.FailErr(t, "restore foreign key enforcement", err)
}

func TestIntegrityFailureQuarantinesEveryWriterDoor(t *testing.T) {
	path, store := uncleanAuditStore(t)
	statement, err := store.PrepareContext(t.Context(), `INSERT INTO store_meta(key,value) VALUES('must-not-write','value')`)
	testutil.FailErr(t, "prepare before audit", err)
	defer func() { _ = statement.Close() }()
	injectIntegrityViolation(t, store)
	// The pending audit must survive without another metadata write.
	_, err = store.writer.Exec(`PRAGMA query_only=ON`)
	testutil.FailErr(t, "refuse diagnostic writes", err)
	if err := AuditIntegrity(t.Context(), store); !errors.Is(err, ErrStoreIncompatible) {
		t.Fatalf("audit = %v", err)
	}
	select {
	case <-store.Failed():
	default:
		t.Fatal("integrity failure did not notify the host")
	}
	if _, err := store.ExecContext(t.Context(), `DELETE FROM store_meta WHERE key='anything'`); !errors.Is(err, ErrStoreIncompatible) {
		t.Fatalf("execute = %v", err)
	}
	if _, err := store.BeginTx(t.Context(), nil); !errors.Is(err, ErrStoreIncompatible) {
		t.Fatalf("transaction = %v", err)
	}
	if _, err := store.PrepareContext(t.Context(), `SELECT 1`); !errors.Is(err, ErrStoreIncompatible) {
		t.Fatalf("prepare = %v", err)
	}
	if _, err := statement.ExecContext(t.Context()); err == nil {
		t.Fatal("retained statement wrote after quarantine")
	}
	testutil.FailErr(t, "close quarantined store", store.Close())
	if _, err := Open(path); !errors.Is(err, ErrStoreIncompatible) {
		t.Fatalf("reopen = %v", err)
	}
}

func TestIncompleteAuditSurvivesCleanShutdown(t *testing.T) {
	path, store := uncleanAuditStore(t)
	injectIntegrityViolation(t, store)
	// An interrupted audit cannot erase its durable obligation.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := AuditIntegrity(ctx, store); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled audit = %v", err)
	}
	if store.Failure() != nil {
		t.Fatal("cancellation classified as damage")
	}
	testutil.FailErr(t, "close before audit completion", store.Close())
	if _, err := Open(path); !errors.Is(err, ErrStoreIncompatible) {
		t.Fatalf("reopen skipped unfinished audit: %v", err)
	}
}

func TestIntegrityQuarantineNotifiesBeforeActiveTransactionDrains(t *testing.T) {
	_, store := uncleanAuditStore(t)
	injectIntegrityViolation(t, store)
	tx, err := store.BeginTx(t.Context(), nil)
	testutil.FailErr(t, "hold writer", err)
	defer func() { _ = tx.Rollback() }()
	done := make(chan error, 1)
	go func() { done <- AuditIntegrity(t.Context(), store) }()
	select {
	case <-store.Failed():
	case <-time.After(5 * time.Second):
		t.Fatal("active transaction blocked the failure signal")
	}
	testutil.FailErr(t, "drain active transaction", tx.Rollback())
	select {
	case err := <-done:
		if !errors.Is(err, ErrStoreIncompatible) {
			t.Fatalf("audit = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("quarantine did not finish draining")
	}
}

func TestAuditOperationalFailureRemainsRetryable(t *testing.T) {
	_, store := uncleanAuditStore(t)
	testutil.FailErr(t, "close audit reader", store.reader.Close())
	if err := RunIntegrityAudit(t.Context(), store); err == nil || errors.Is(err, ErrStoreIncompatible) {
		t.Fatalf("operational failure = %v", err)
	}
	if store.Failure() != nil {
		t.Fatal("reader failure quarantined the store")
	}
	select {
	case <-store.Failed():
		t.Fatal("reader failure notified permanent damage")
	default:
	}
	if err := integrityCheckError(sql.ErrConnDone, "audit"); !errors.Is(err, sql.ErrConnDone) {
		t.Fatalf("lost diagnostic cause: %v", err)
	}
}
