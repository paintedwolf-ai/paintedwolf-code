package db

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	// Confirmed damage must survive even when SQLite cannot save its diagnosis.
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
	rejected, err := store.PrepareContext(t.Context(), `SELECT 1`)
	if rejected != nil {
		defer func() { _ = rejected.Close() }()
	}
	if !errors.Is(err, ErrStoreIncompatible) {
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
	reopened, err := Open(path)
	testutil.FailErr(t, "reopen without blocking on unfinished audit", err)
	t.Cleanup(func() { _ = reopened.Close() })
	if !reopened.IntegrityAuditDue() {
		t.Fatal("reopen lost its audit obligation")
	}
	if err := AuditIntegrity(t.Context(), reopened); !errors.Is(err, ErrStoreIncompatible) {
		t.Fatalf("resumed audit missed damage: %v", err)
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

func TestStartupDefersWholeStoreScan(t *testing.T) {
	path, store := uncleanAuditStore(t)
	var page, pageSize int64
	testutil.FailErr(t, "locate unused data page", store.QueryRowContext(t.Context(), `SELECT rootpage FROM sqlite_schema WHERE name='project_roots'`).Scan(&page))
	testutil.FailErr(t, "read page size", store.QueryRowContext(t.Context(), `PRAGMA page_size`).Scan(&pageSize))
	testutil.FailErr(t, "close without finishing audit", store.Close())
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	testutil.FailErr(t, "open corruption fixture", err)
	// Damage a data B-tree, leaving SQLite's schema and store metadata readable.
	_, err = file.WriteAt([]byte{0}, (page-1)*pageSize)
	testutil.FailErr(t, "damage unused data page", err)
	testutil.FailErr(t, "close corruption fixture", file.Close())
	store, err = Open(path)
	testutil.FailErr(t, "open without whole-store check", err)
	t.Cleanup(func() { _ = store.Close() })
	if !store.IntegrityAuditDue() {
		t.Fatal("audit obligation lost")
	}
	var result string
	err = store.reader.QueryRowContext(t.Context(), `PRAGMA quick_check`).Scan(&result)
	if err == nil && result == "ok" {
		t.Fatal("fixture does not detect a blocking quick_check regression")
	}
	if err := AuditIntegrity(t.Context(), store); !errors.Is(err, ErrStoreIncompatible) {
		t.Fatalf("audit = %v", err)
	}
	testutil.FailErr(t, "close damaged store", store.Close())
	if _, err := Open(path); !errors.Is(err, ErrStoreIncompatible) {
		t.Fatalf("confirmed damage reopened: %v", err)
	}
}

func TestLegacyAuditState(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		refuse      bool
	}{
		{"unfinished", integrityAuditPendingValue, false},
		{"confirmed", "foreign_key_check failed: project_roots references projects", true},
		{"unknown nonempty", "unrecognized diagnostic", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, store := uncleanAuditStore(t)
			_, err := store.writer.Exec(`UPDATE store_meta SET value=? WHERE key=?`, tc.value, integrityAuditFailedMetaKey)
			testutil.FailErr(t, "seed shipped marker", err)
			testutil.FailErr(t, "close shipped fixture", store.Close())
			reopened, err := Open(path)
			if tc.refuse {
				if !errors.Is(err, ErrStoreIncompatible) {
					if reopened != nil {
						_ = reopened.Close()
					}
					t.Fatalf("open = %v", err)
				}
				return
			}
			testutil.FailErr(t, "open unfinished audit", err)
			t.Cleanup(func() { _ = reopened.Close() })
			if !reopened.IntegrityAuditDue() {
				t.Fatal("pending audit lost")
			}
			testutil.FailErr(t, "complete audit", AuditIntegrity(t.Context(), reopened))
			testutil.FailErr(t, "close verified store", reopened.Close())
			reopened, err = Open(path)
			testutil.FailErr(t, "reopen verified store", err)
			if reopened.IntegrityAuditDue() {
				t.Fatal("finished audit scheduled again")
			}
		})
	}
}

func TestQuarantineIsBoundToPhysicalStore(t *testing.T) {
	path, store := uncleanAuditStore(t)
	injectIntegrityViolation(t, store)
	if err := AuditIntegrity(t.Context(), store); !errors.Is(err, ErrStoreIncompatible) {
		t.Fatalf("audit = %v", err)
	}
	testutil.FailErr(t, "close quarantined store", store.Close())
	before, err := os.ReadFile(path)
	testutil.FailErr(t, "read before refusal", err)
	if _, err := Open(path); !errors.Is(err, ErrStoreIncompatible) {
		t.Fatalf("open damaged store = %v", err)
	}
	after, err := os.ReadFile(path)
	testutil.FailErr(t, "read after refusal", err)
	if !bytes.Equal(before, after) {
		t.Fatal("refusal modified damaged database")
	}
	replacement := filepath.Join(t.TempDir(), "healthy.db")
	healthy, err := Open(replacement)
	testutil.FailErr(t, "create replacement", err)
	testutil.FailErr(t, "close replacement", healthy.Close())
	testutil.FailErr(t, "preserve old physical store", os.Rename(path, path+".old"))
	testutil.FailErr(t, "install replacement", os.Rename(replacement, path))
	healthy, err = Open(path)
	testutil.FailErr(t, "open replaced store despite old quarantine", err)
	testutil.FailErr(t, "close replaced store", healthy.Close())
}

func TestQuarantineRecordRefusesInvalidOrUnreadableEvidence(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"malformed", "{"}, {"future version", `{"version":2,"identity":"old","detail":"damage"}`},
		{"oversized", strings.Repeat("x", integrityFailureMaxBytes+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, store := uncleanAuditStore(t)
			testutil.FailErr(t, "close store", store.Close())
			testutil.FailErr(t, "write quarantine fixture", os.WriteFile(path+IntegrityFailureSuffix, []byte(tc.body), 0o600))
			if _, err := Open(path); !errors.Is(err, ErrStoreIncompatible) {
				t.Fatalf("open invalid quarantine=%v", err)
			}
		})
	}
	path, store := uncleanAuditStore(t)
	testutil.FailErr(t, "close store", store.Close())
	testutil.FailErr(t, "block quarantine path", os.Mkdir(path+IntegrityFailureSuffix, 0o700))
	if _, err := Open(path); !errors.Is(err, ErrStoreIncompatible) {
		t.Fatalf("open unreadable quarantine=%v", err)
	}
}

func TestCrashRecoveryPreservesCommittedWALAndDiscardsUncommittedWork(t *testing.T) {
	if path := os.Getenv("PW_TEST_CRASH_AUDIT_DB"); path != "" {
		store, err := Open(path)
		testutil.FailErr(t, "open child store", err)
		_, err = store.ExecContext(t.Context(), `INSERT INTO store_meta(key,value) VALUES('committed_crash_probe','yes')`)
		testutil.FailErr(t, "commit child data", err)
		tx, err := store.BeginTx(t.Context(), nil)
		testutil.FailErr(t, "begin interrupted transaction", err)
		_, err = tx.ExecContext(t.Context(), `INSERT INTO store_meta(key,value) VALUES('uncommitted_crash_probe','no')`)
		testutil.FailErr(t, "write uncommitted child data", err)
		os.Exit(0) // Simulate process loss without closing the transaction or store.
	}
	path := filepath.Join(t.TempDir(), "store.db")
	executable, err := os.Executable()
	testutil.FailErr(t, "locate test binary", err)
	child := exec.Command(executable, "-test.run=^TestCrashRecoveryPreservesCommittedWALAndDiscardsUncommittedWork$")
	child.Env = append(os.Environ(), "PW_TEST_CRASH_AUDIT_DB="+path)
	output, err := child.CombinedOutput()
	if err != nil {
		t.Fatalf("crash fixture=%v: %s", err, output)
	}
	store, err := Open(path)
	testutil.FailErr(t, "recover child WAL", err)
	t.Cleanup(func() { _ = store.Close() })
	if !store.IntegrityAuditDue() {
		t.Fatal("crash did not schedule an audit")
	}
	var committed, uncommitted int
	testutil.FailErr(t, "read committed WAL", store.QueryRowContext(t.Context(), `SELECT count(*) FROM store_meta WHERE key='committed_crash_probe'`).Scan(&committed))
	testutil.FailErr(t, "read interrupted transaction", store.QueryRowContext(t.Context(), `SELECT count(*) FROM store_meta WHERE key='uncommitted_crash_probe'`).Scan(&uncommitted))
	if committed != 1 || uncommitted != 0 {
		t.Fatalf("committed=%d, uncommitted=%d", committed, uncommitted)
	}
	testutil.FailErr(t, "audit recovered store", AuditIntegrity(t.Context(), store))
}

func TestQuarantinePublicationFailureStillStopsWrites(t *testing.T) {
	path, store := uncleanAuditStore(t)
	injectIntegrityViolation(t, store)
	testutil.FailErr(t, "block quarantine publication", os.Mkdir(path+IntegrityFailureSuffix, 0o700))
	if err := AuditIntegrity(t.Context(), store); !errors.Is(err, ErrStoreIncompatible) {
		t.Fatalf("audit=%v", err)
	}
	select {
	case <-store.Failed():
	default:
		t.Fatal("failure publication blocked host shutdown")
	}
	if _, err := store.ExecContext(t.Context(), `INSERT INTO store_meta(key,value) VALUES('unsafe','unsafe')`); !errors.Is(err, ErrStoreIncompatible) {
		t.Fatalf("write after failure=%v", err)
	}
}
