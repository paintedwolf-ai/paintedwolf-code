package db

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestProjectSessionStatsFollowSessionLifecycle(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "store.db"))
	testutil.FailErr(t, "open store", err)
	t.Cleanup(func() { _ = store.Close() })
	ctx := t.Context()
	for _, id := range []string{"project-a", "project-b"} {
		_, err := store.ExecContext(ctx, `
			INSERT INTO projects (id, last_opened_at, created_at)
			VALUES (?, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, id)
		testutil.FailErr(t, "insert "+id, err)
	}
	insertSession := func(id, projectID, activityAt string) {
		t.Helper()
		_, err := store.ExecContext(ctx, `
			INSERT INTO sessions (id, project_id, owner_person_id, posture, created_at, activity_at, updated_at)
			VALUES (?, ?, (SELECT id FROM people WHERE role = 'owner'), 'build', '2026-01-01T00:00:00Z', ?, ?)`,
			id, projectID, activityAt, activityAt)
		testutil.FailErr(t, "insert "+id, err)
	}
	stats := func(projectID string) (int64, sql.NullString) {
		t.Helper()
		var count int64
		var activity sql.NullString
		err := store.QueryRowContext(ctx, `
			SELECT session_count, last_session_activity_at FROM projects WHERE id = ?`, projectID).
			Scan(&count, &activity)
		testutil.FailErr(t, "read stats "+projectID, err)
		return count, activity
	}

	insertSession("session-old", "project-a", "2026-01-02T00:00:00Z")
	insertSession("session-new", "project-a", "2026-01-03T00:00:00Z")
	if count, activity := stats("project-a"); count != 2 || activity.String != "2026-01-03T00:00:00Z" {
		t.Fatalf("insert stats = (%d, %q)", count, activity.String)
	}
	_, err = store.ExecContext(ctx, `UPDATE sessions SET updated_at = '2026-01-05T00:00:00Z' WHERE id = 'session-old'`)
	testutil.FailErr(t, "update record", err)
	if _, activity := stats("project-a"); activity.String != "2026-01-03T00:00:00Z" {
		t.Fatalf("a record change moved project activity to %q", activity.String)
	}
	_, err = store.ExecContext(ctx, `UPDATE sessions SET activity_at = '2026-01-04T00:00:00Z' WHERE id = 'session-old'`)
	testutil.FailErr(t, "update activity", err)
	if _, activity := stats("project-a"); activity.String != "2026-01-04T00:00:00Z" {
		t.Fatalf("updated activity = %q", activity.String)
	}
	_, err = store.ExecContext(ctx, `UPDATE sessions SET project_id = 'project-b' WHERE id = 'session-old'`)
	testutil.FailErr(t, "move session", err)
	if count, activity := stats("project-a"); count != 1 || activity.String != "2026-01-03T00:00:00Z" {
		t.Fatalf("source stats after move = (%d, %q)", count, activity.String)
	}
	if count, activity := stats("project-b"); count != 1 || activity.String != "2026-01-04T00:00:00Z" {
		t.Fatalf("destination stats after move = (%d, %q)", count, activity.String)
	}
	_, err = store.ExecContext(ctx, `DELETE FROM sessions WHERE id = 'session-old'`)
	testutil.FailErr(t, "delete session", err)
	if count, activity := stats("project-b"); count != 0 || activity.Valid {
		t.Fatalf("empty project stats = (%d, %#v)", count, activity)
	}
}

func TestOpenCreatesAllSchemaSQLTables(t *testing.T) {
	schema, err := schemaFS.ReadFile("schema.sql")
	testutil.FailErr(t, "read schema.sql", err)
	re := regexp.MustCompile(`CREATE TABLE IF NOT EXISTS (\w+)`)
	matches := re.FindAllStringSubmatch(string(schema), -1)
	if len(matches) == 0 {
		t.Fatal("schema.sql: expected CREATE TABLE statements")
	}

	dbPath := filepath.Join(t.TempDir(), "store.db")
	sqlDB, err := Open(dbPath)
	testutil.FailErr(t, "Open failed", err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	for _, m := range matches {
		table := m[1]
		var count int
		q := fmt.Sprintf("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='%s'", table)
		if err := sqlDB.QueryRowContext(t.Context(), q).Scan(&count); err != nil {
			testutil.FailErr(t, "query table "+table, err)
		}
		if count != 1 {
			t.Fatalf("table %q missing after Open", table)
		}
	}
}

func TestOpenCorruptStoreRoutesToRecovery(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "store.db")
	testutil.FailErr(t, "write corrupt store",
		os.WriteFile(dbPath, []byte(strings.Repeat("not a database ", 64)), 0o600))

	_, err := Open(dbPath)
	if err == nil {
		t.Fatal("Open succeeded on a corrupt store")
	}
	var incompatible *StoreIncompatibleError
	if !errors.As(err, &incompatible) {
		t.Fatalf("Open error = %v; want StoreIncompatibleError", err)
	}
	if incompatible.Reason != RecoveryReasonIntegrityFailed {
		t.Fatalf("reason = %q want %q", incompatible.Reason, RecoveryReasonIntegrityFailed)
	}
}

func TestOpenEmptyStoreWithoutRecoveryArtifactsInitializesBaseline(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "store.db")
	testutil.FailErr(t, "write empty store", os.WriteFile(dbPath, nil, 0o600))

	store, err := Open(dbPath)
	testutil.FailErr(t, "open empty store as fresh", err)
	t.Cleanup(func() { _ = store.Close() })
	version, err := ReadUserVersion(t.Context(), store)
	testutil.FailErr(t, "read initialized baseline", err)
	if version != SchemaVersion {
		t.Fatalf("user_version = %d want %d", version, SchemaVersion)
	}
	info, err := os.Stat(dbPath)
	testutil.FailErr(t, "stat initialized store", err)
	if info.Size() == 0 {
		t.Fatal("initialized store stayed empty")
	}
}

func TestOpenEmptyStoreBesideJournalRoutesToRecovery(t *testing.T) {
	for _, suffix := range []string{"-wal", "-shm"} {
		t.Run(suffix, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "store.db")
			testutil.FailErr(t, "write empty store", os.WriteFile(dbPath, nil, 0o600))
			testutil.FailErr(t, "write recovery artifact", os.WriteFile(dbPath+suffix, []byte("present"), 0o600))

			_, err := Open(dbPath)
			var incompatible *StoreIncompatibleError
			if !errors.As(err, &incompatible) {
				t.Fatalf("Open error = %v; want StoreIncompatibleError", err)
			}
			if incompatible.Reason != RecoveryReasonIntegrityFailed {
				t.Fatalf("reason = %q want %q", incompatible.Reason, RecoveryReasonIntegrityFailed)
			}
		})
	}
}

func TestOpenEmptyStoreBesideUpgradeRecoveryRoutesToRecovery(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "store.db")
	testutil.FailErr(t, "write empty store", os.WriteFile(path, nil, 0o600))
	testutil.FailErr(t, "create upgrade recovery", os.Mkdir(filepath.Join(root, "upgrade-recovery"), 0o700))
	_, err := Open(path)
	if !errors.Is(err, ErrStoreIncompatible) {
		t.Fatalf("open ambiguous empty store = %v", err)
	}
}

func TestOpenTracksShutdownState(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "store.db")
	store, err := Open(dbPath)
	testutil.FailErr(t, "open fresh store", err)
	clean, err := lastShutdownWasClean(t.Context(), store.writer)
	testutil.FailErr(t, "read open state", err)
	if clean {
		t.Fatal("open store is marked clean")
	}
	testutil.FailErr(t, "clean close", store.Close())

	writer, err := openWriter(t.Context(), dbPath, StoreOptions{})
	testutil.FailErr(t, "open writer", err)
	clean, err = lastShutdownWasClean(t.Context(), writer)
	testutil.FailErr(t, "read closed state", err)
	if !clean {
		t.Fatal("closed store is marked unclean")
	}
	testutil.FailErr(t, "close writer", writer.Close())

	store, err = Open(dbPath)
	testutil.FailErr(t, "reopen clean store", err)
	t.Cleanup(func() { _ = store.Close() })
	clean, err = lastShutdownWasClean(t.Context(), store.writer)
	testutil.FailErr(t, "read reopened state", err)
	if clean {
		t.Fatal("reopened store is marked clean")
	}
}

// An unclean shutdown costs the boot path a structural quick_check, not a
// whole-store scan. The whole-store audit runs behind serving; damage it finds
// is stamped so the next boot runs the full check synchronously and refuses.
func TestUncleanShutdownAuditsBehindBootAndRefusesNextOpen(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "store.db")
	store, err := Open(dbPath)
	testutil.FailErr(t, "open store", err)
	if store.IntegrityAuditDue() {
		t.Fatal("a fresh store owes no audit")
	}
	testutil.FailErr(t, "close store", store.Close())

	writer, err := openWriter(t.Context(), dbPath, StoreOptions{})
	testutil.FailErr(t, "open writer", err)
	_, err = writer.Exec(`PRAGMA foreign_keys = OFF`)
	testutil.FailErr(t, "disable foreign keys", err)
	_, err = writer.Exec(`
		INSERT INTO project_roots (id, project_id, path, label, added_at)
		VALUES ('invalid', 'missing', '/tmp/invalid', 'invalid', '2026-01-01T00:00:00Z')
	`)
	testutil.FailErr(t, "insert invalid row", err)
	testutil.FailErr(t, "mark unclean shutdown", markShutdownState(t.Context(), writer, false))
	testutil.FailErr(t, "close writer", writer.Close())

	store, err = Open(dbPath)
	testutil.FailErr(t, "open after unclean shutdown", err)
	if !store.IntegrityAuditDue() {
		t.Fatal("an unclean shutdown must leave the whole-store audit due")
	}
	auditErr := AuditIntegrity(t.Context(), store)
	var incompatible *StoreIncompatibleError
	if !errors.As(auditErr, &incompatible) || incompatible.Reason != RecoveryReasonIntegrityFailed {
		t.Fatalf("audit error = %v; want integrity failure", auditErr)
	}
	testutil.FailErr(t, "close audited store", store.Close())

	_, err = Open(dbPath)
	if !errors.As(err, &incompatible) {
		t.Fatalf("Open error = %v; want StoreIncompatibleError after a failed audit", err)
	}
	if incompatible.Reason != RecoveryReasonIntegrityFailed {
		t.Fatalf("reason = %q want %q", incompatible.Reason, RecoveryReasonIntegrityFailed)
	}
}

// A clean audit clears the stamp, and a clean shutdown owes no audit at all.
func TestCleanAuditClearsStampAndCleanShutdownOwesNoAudit(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "store.db")
	store, err := Open(dbPath)
	testutil.FailErr(t, "open store", err)
	testutil.FailErr(t, "stamp a stale failure", stampIntegrityAuditFailed(t.Context(), store.writer, errors.New("stale")))
	testutil.FailErr(t, "audit healthy store", AuditIntegrity(t.Context(), store))
	stamped, err := integrityAuditFailed(t.Context(), store.writer)
	testutil.FailErr(t, "read stamp", err)
	if stamped {
		t.Fatal("a clean audit must clear the failure stamp")
	}
	testutil.FailErr(t, "close store", store.Close())

	store, err = Open(dbPath)
	testutil.FailErr(t, "reopen after clean shutdown", err)
	t.Cleanup(func() { _ = store.Close() })
	if store.IntegrityAuditDue() {
		t.Fatal("a clean shutdown owes no audit")
	}
}

func TestOpenRefusesBaselineMismatchWithoutChangingStore(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "store.db")
	sqlDB, err := Open(dbPath)
	testutil.FailErr(t, "open baseline store", err)
	testutil.FailErr(t, "close baseline store", sqlDB.Close())

	writer, err := openWriter(t.Context(), dbPath, StoreOptions{})
	testutil.FailErr(t, "open writer", err)
	_, err = writer.Exec(`PRAGMA user_version = 2`)
	testutil.FailErr(t, "change baseline marker", err)
	testutil.FailErr(t, "close writer", writer.Close())
	before, err := os.ReadFile(dbPath)
	testutil.FailErr(t, "read mismatched store", err)

	_, err = Open(dbPath)
	if err == nil {
		t.Fatal("Open accepted a mismatched baseline")
	}
	var incompatible *StoreIncompatibleError
	if !errors.As(err, &incompatible) {
		t.Fatalf("Open error = %v; want StoreIncompatibleError", err)
	}
	if incompatible.Reason != RecoveryReasonSchemaMismatch {
		t.Fatalf("reason = %q want %q", incompatible.Reason, RecoveryReasonSchemaMismatch)
	}
	after, err := os.ReadFile(dbPath)
	testutil.FailErr(t, "reread mismatched store", err)
	if !bytes.Equal(after, before) {
		t.Fatal("Open changed a mismatched store")
	}

	writer, err = openWriter(t.Context(), dbPath, StoreOptions{})
	testutil.FailErr(t, "reopen unchanged store", err)
	t.Cleanup(func() { _ = writer.Close() })
	version, err := ReadUserVersion(t.Context(), writer)
	testutil.FailErr(t, "read baseline marker", err)
	if version != 2 {
		t.Fatalf("user_version = %d want preserved mismatch 2", version)
	}
}

func TestOpenPreservesStoreWithMismatchedShape(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "store.db")
	sqlDB, err := Open(dbPath)
	testutil.FailErr(t, "open baseline store", err)
	testutil.FailErr(t, "close baseline store", sqlDB.Close())

	writer, err := openWriter(t.Context(), dbPath, StoreOptions{})
	testutil.FailErr(t, "open writer", err)
	_, err = writer.Exec(`ALTER TABLE projects ADD COLUMN unexpected TEXT`)
	testutil.FailErr(t, "change baseline shape", err)
	testutil.FailErr(t, "close writer", writer.Close())

	_, err = Open(dbPath)
	if err == nil {
		t.Fatal("Open accepted a mismatched shape")
	}
	var incompatible *StoreIncompatibleError
	if !errors.As(err, &incompatible) {
		t.Fatalf("Open error = %v; want StoreIncompatibleError", err)
	}
	if incompatible.Reason != RecoveryReasonSchemaMismatch {
		t.Fatalf("reason = %q want %q", incompatible.Reason, RecoveryReasonSchemaMismatch)
	}

	writer, err = openWriter(t.Context(), dbPath, StoreOptions{})
	testutil.FailErr(t, "reopen unchanged store", err)
	t.Cleanup(func() { _ = writer.Close() })
	var columns int
	err = writer.QueryRow(`SELECT count(*) FROM pragma_table_info('projects') WHERE name = 'unexpected'`).Scan(&columns)
	testutil.FailErr(t, "read preserved shape", err)
	if columns != 1 {
		t.Fatalf("unexpected columns = %d want preserved column", columns)
	}
}

func TestOpenDatabaseFileMode0600(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "nested", "store.db")
	sqlDB, err := Open(dbPath)
	testutil.FailErr(t, "Open failed", err)
	sqlDB.Close()

	info, err := os.Stat(dbPath)
	testutil.FailErr(t, "stat path", err)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o want 0600", info.Mode().Perm())
	}
}

func TestDefaultPath(t *testing.T) {
	path, err := DefaultPath()
	testutil.FailErr(t, "DefaultPath failed", err)
	if !strings.Contains(path, "store.db") {
		t.Fatalf("path = %q", path)
	}
}

func TestOpenUsesWALMode(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "wal.db")
	sqlDB, err := Open(dbPath)
	testutil.FailErr(t, "Open failed", err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	var mode string
	if err := sqlDB.QueryRowContext(t.Context(), `PRAGMA journal_mode`).Scan(&mode); err != nil {
		testutil.FailErr(t, "PRAGMA journal_mode", err)
	}
	if mode != "wal" {
		t.Fatalf("journal_mode = %q want wal", mode)
	}
	stats := sqlDB.Stats()
	if stats.Writer.MaxOpenConnections != writerOpenConns {
		t.Fatalf("writer MaxOpenConnections = %d want %d", stats.Writer.MaxOpenConnections, writerOpenConns)
	}
	if stats.Reader.MaxOpenConnections != readerOpenConns {
		t.Fatalf("reader MaxOpenConnections = %d want %d", stats.Reader.MaxOpenConnections, readerOpenConns)
	}
}

func TestOpenExistingDatabase(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "existing.db")
	sqlDB, err := Open(dbPath)
	testutil.FailErr(t, "Open failed", err)
	sqlDB.Close()

	sqlDB, err = Open(dbPath)
	testutil.FailErr(t, "Open failed", err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	var n int
	if err := sqlDB.QueryRowContext(t.Context(), `SELECT count(*) FROM checkpoints`).Scan(&n); err != nil {
		testutil.FailErr(t, "sqlDB.QueryRow failed", err)
	}
}

func TestOpenReadOnlyRejectsWrites(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "readonly.db")
	writer, err := Open(dbPath)
	testutil.FailErr(t, "open writable store", err)
	testutil.FailErr(t, "close writable store", writer.Close())

	reader, err := OpenReadOnly(t.Context(), dbPath)
	testutil.FailErr(t, "open read-only store", err)
	defer reader.Close()
	if _, err := reader.ExecContext(t.Context(), `CREATE TABLE must_not_exist (id TEXT)`); err == nil {
		t.Fatal("read-only database accepted a schema write")
	}
}

func TestFastTestSyncWriterCacheSizeInvariant(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "fastsync.db")
	store, err := OpenWithOptions(t.Context(), dbPath, UpgradeHooks{}, StoreOptions{FastTestSync: true})
	testutil.FailErr(t, "OpenWithOptions with FastTestSync", err)
	t.Cleanup(func() { _ = store.Close() })

	var cacheSize int
	testutil.FailErr(t, "query cache_size", store.writer.QueryRowContext(t.Context(), `PRAGMA cache_size`).Scan(&cacheSize))
	if cacheSize != -2000 {
		t.Fatalf("FastTestSync cache_size = %d, want -2000 (2 MiB budget)", cacheSize)
	}
}
