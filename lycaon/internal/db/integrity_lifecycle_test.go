package db

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestPendingAuditKeepsWritesCheckpointsAndRestartAvailable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	store := auditLifecycleStore(t, path, false)
	for _, cause := range []error{context.Canceled, syscall.EIO} {
		if err := finishIntegrityAudit(t.Context(), store, cause); !errors.Is(err, cause) {
			t.Fatalf("unfinished audit cause=%v, want %v", err, cause)
		}
		assertAuditPending(t, store, true)
		if store.Failure() != nil {
			t.Fatal("unfinished verification quarantined ordinary use")
		}
	}
	_, err := store.ExecContext(t.Context(), "INSERT INTO audit_rows VALUES(3,'ordinary write')")
	testutil.FailErr(t, "write while exhaustive audit is pending", err)
	checkpoint, err := CheckpointWAL(t.Context(), store)
	testutil.FailErr(t, "checkpoint while cleanup waits", err)
	if !checkpoint.Complete() {
		t.Fatal("cleanup gate retained a checkpoint-blocking snapshot")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := store.WaitForIntegrityAudit(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled cleanup wait=%v", err)
	}
	assertAuditPending(t, store, true)
	testutil.FailErr(t, "close pending readers", store.reader.Close())
	testutil.FailErr(t, "close pending writer", store.writer.Close())
	store = reopenAuditLifecycleStore(t, path)
	assertAuditPending(t, store, true)
	completed := make(chan error, 1)
	go func() { completed <- store.WaitForIntegrityAudit(t.Context()) }()
	testutil.FailErr(t, "complete exhaustive background verification", AuditIntegrity(t.Context(), store))
	testutil.FailErr(t, "release cleanup after audit", <-completed)
	assertAuditPending(t, store, false)
	testutil.FailErr(t, "healthy cleanup does not wait", store.WaitForIntegrityAudit(t.Context()))
}

func TestSilentSecondaryIndexOmissionNeverAuthorizesCleanup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	store := auditLifecycleStore(t, path, true)
	var primary, indexed int
	testutil.FailErr(t, "read retained primary value", store.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM audit_rows NOT INDEXED WHERE value='kept'").Scan(&primary))
	testutil.FailErr(t, "read silently incomplete secondary index", store.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM audit_rows INDEXED BY audit_values WHERE value='kept'").Scan(&indexed))
	if primary != 1 || indexed != 0 {
		t.Fatalf("fixture is not a silent omission: primary=%d indexed=%d", primary, indexed)
	}
	testutil.FailErr(t, "structural check preserves pending state", scanCheck(t.Context(), store.reader, "PRAGMA quick_check"))
	assertAuditPending(t, store, true)
	cleanup := make(chan error, 1)
	go func() { cleanup <- store.WaitForIntegrityAudit(t.Context()) }()
	err := AuditIntegrity(t.Context(), store)
	var damage *StoreIncompatibleError
	if !errors.As(err, &damage) || damage.Reason != RecoveryReasonIntegrityFailed {
		t.Fatalf("exhaustive audit did not diagnose omitted index entry: %v", err)
	}
	if err := <-cleanup; !errors.Is(err, ErrStoreIncompatible) {
		t.Fatalf("damaged index released cleanup: %v", err)
	}
	if _, err := store.ExecContext(t.Context(), "INSERT INTO audit_rows VALUES(4,'forbidden')"); !errors.Is(err, ErrStoreIncompatible) {
		t.Fatalf("confirmed damage left writes available: %v", err)
	}
	if err := CheckStoreHealth(path); !errors.Is(err, ErrStoreIncompatible) {
		t.Fatalf("restart lost confirmed quarantine: %v", err)
	}
	state, err := integrityAuditState(t.Context(), store.reader)
	testutil.FailErr(t, "read retained audit obligation", err)
	if state != integrityAuditPendingValue {
		t.Fatal("failed audit cleared its durable obligation")
	}
}

func TestUnreadableAuditStateDoesNotBecomeCleanupPermission(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	store := auditLifecycleStore(t, path, false)
	testutil.FailErr(t, "close state reader", store.reader.Close())
	if _, err := store.IntegrityAuditPending(t.Context()); err == nil {
		t.Fatal("unreadable state authorized cleanup")
	}
	testutil.FailErr(t, "close state writer", store.writer.Close())
	store = reopenAuditLifecycleStore(t, path)
	assertAuditPending(t, store, true)
}

func auditLifecycleStore(t *testing.T, path string, omitIndexRow bool) *Store {
	t.Helper()
	database, err := sql.Open("sqlite", path)
	testutil.FailErr(t, "create audit lifecycle fixture", err)
	_, err = database.Exec(`CREATE TABLE store_meta(key TEXT PRIMARY KEY,value TEXT NOT NULL);
 CREATE TABLE audit_rows(id INTEGER PRIMARY KEY,value TEXT NOT NULL);
 INSERT INTO audit_rows VALUES(1,'kept'),(2,'visible');
 CREATE INDEX audit_values ON audit_rows(substr(value,2))`)
	testutil.FailErr(t, "seed audit lifecycle rows", err)
	if omitIndexRow {
		// Preserve index cardinality and valid page structure while omitting
		// the declared keys, so the defect needs the exhaustive cross-check.
		_, err = database.Exec(`PRAGMA writable_schema=ON;
 UPDATE sqlite_schema SET sql='CREATE INDEX audit_values ON audit_rows(value)' WHERE name='audit_values';
 PRAGMA writable_schema=OFF`)
	} else {
		_, err = database.Exec("DROP INDEX audit_values; CREATE INDEX audit_values ON audit_rows(value)")
	}
	testutil.FailErr(t, "select secondary index consistency", err)
	testutil.FailErr(t, "close fixture schema cache", database.Close())
	store := reopenAuditLifecycleStore(t, path)
	testutil.FailErr(t, "persist audit obligation", prepareAuditState(t.Context(), store.writer, true))
	return store
}

func reopenAuditLifecycleStore(t *testing.T, path string) *Store {
	t.Helper()
	writer, err := openWriter(t.Context(), path, StoreOptions{FastTestSync: true})
	testutil.FailErr(t, "open audit lifecycle writer", err)
	reader, err := openReader(t.Context(), path)
	testutil.FailErr(t, "open audit lifecycle reader", err)
	store := newStore(writer, reader)
	testutil.FailErr(t, "bind physical audit custody", bindStoreHealth(store, path))
	t.Cleanup(func() { _ = reader.Close(); _ = writer.Close() })
	return store
}

func assertAuditPending(t *testing.T, store *Store, want bool) {
	t.Helper()
	got, err := store.IntegrityAuditPending(t.Context())
	testutil.FailErr(t, "inspect durable cleanup obligation", err)
	if got != want {
		t.Fatalf("audit pending=%v, want %v", got, want)
	}
}
