package db

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

const auditPinnedFrameLimit = 1024

func TestAuditRetriesWholeSnapshotsWithoutPartialCredit(t *testing.T) {
	_, store := uncleanAuditStore(t)
	attempts, waits := 0, 0
	testutil.FailErr(t, "finish deferred audit", auditAttempts(t.Context(),
		func(ctx context.Context) error {
			attempts++
			if attempts <= 3 {
				return errAuditDeferred
			}
			return auditSnapshot(ctx, store.reader, nil)
		}, func(ctx context.Context) error {
			waits++
			state, err := integrityAuditState(ctx, store.reader)
			testutil.FailErr(t, "read pending audit", err)
			if state != integrityAuditPendingValue || store.Failure() != nil {
				t.Fatal("unfinished audit lost obligation or quarantined store")
			}
			return nil
		}))
	if waits != 3 || attempts != 4 {
		t.Fatalf("attempts=%d waits=%d", attempts, waits)
	}
	testutil.FailErr(t, "complete production audit", AuditIntegrity(t.Context(), store))
}

func TestAuditCheckpointPressureReleasesOnlyItsRead(t *testing.T) {
	_, store := uncleanAuditStore(t)
	ctx, finish := store.audit.begin(t.Context(), auditPinnedFrameLimit)
	defer finish()
	if _, limited := ctx.Deadline(); limited {
		t.Fatal("audit imposed a machine-speed deadline")
	}
	store.audit.checkpoint(WALCheckpoint{LogFrames: auditPinnedFrameLimit - 1})
	if ctx.Err() != nil {
		t.Fatal("audit yielded below its pinned-frame budget")
	}
	store.audit.checkpoint(WALCheckpoint{LogFrames: auditPinnedFrameLimit + 1})
	if !errors.Is(context.Cause(ctx), errAuditDeferred) {
		t.Fatalf("audit pressure cause = %v", context.Cause(ctx))
	}
	if store.Failure() != nil || t.Context().Err() != nil {
		t.Fatal("audit pressure affected application health or caller context")
	}
	finish()
	next, release := store.audit.begin(t.Context(), auditPinnedFrameLimit)
	defer release()
	if next.Err() != nil {
		t.Fatal("previous pressure prevented the next read")
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := waitForAuditCapacity(canceled, store, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel idle wait = %v", err)
	}
	if err := auditSnapshot(canceled, store.reader, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel audit = %v", err)
	}
}

func TestRealCheckpointPressureInterruptsAuditAndThenCompletes(t *testing.T) {
	_, store := uncleanAuditStore(t)
	ctx, finish := store.audit.begin(t.Context(), auditPinnedFrameLimit)
	conn, err := store.reader.Conn(t.Context())
	testutil.FailErr(t, "open audit read", err)
	defer conn.Close()
	_, err = conn.ExecContext(ctx, "BEGIN")
	testutil.FailErr(t, "hold audit snapshot", err)
	var count int
	testutil.FailErr(t, "read audit snapshot", conn.QueryRowContext(ctx, "SELECT count(*) FROM code_scans").Scan(&count))
	growWAL(t, store, "audit-pressure", 200)
	result, err := CheckpointWAL(t.Context(), store)
	testutil.FailErr(t, "observe actual pinned frames", err)
	if result.PinnedFrames() < auditPinnedFrameLimit {
		t.Fatalf("fixture pinned only %d frames", result.PinnedFrames())
	}
	if !errors.Is(context.Cause(ctx), errAuditDeferred) {
		t.Fatalf("checkpoint did not release audit: %v", context.Cause(ctx))
	}
	_, err = conn.ExecContext(t.Context(), "ROLLBACK")
	testutil.FailErr(t, "release interrupted read", err)
	finish()
	result, err = CheckpointWAL(t.Context(), store)
	testutil.FailErr(t, "checkpoint released frames", err)
	if !result.Complete() {
		t.Fatal("interrupted audit still pins the WAL")
	}
	testutil.FailErr(t, "finish audit after pressure", AuditIntegrity(t.Context(), store))
	state, err := integrityAuditState(t.Context(), store.reader)
	testutil.FailErr(t, "read final audit state", err)
	if state != "" || store.Failure() != nil {
		t.Fatalf("audit failed to complete: state=%q failure=%v", state, store.Failure())
	}
}

func TestWholeFileAuditDetectsSharedTablePages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aliased.db")
	writer, err := sql.Open("sqlite", path)
	testutil.FailErr(t, "open page ownership fixture", err)
	_, err = writer.Exec(`CREATE TABLE first(value TEXT); CREATE TABLE second(value TEXT);
 INSERT INTO first VALUES('retained'); INSERT INTO second VALUES('other');
 PRAGMA writable_schema=ON;
 UPDATE sqlite_schema SET rootpage=(SELECT rootpage FROM sqlite_schema WHERE name='first') WHERE name='second';
 PRAGMA writable_schema=OFF`)
	testutil.FailErr(t, "alias table roots", err)
	testutil.FailErr(t, "close fixture writer", writer.Close())
	reader, err := sql.Open("sqlite", fileDSN(path, "mode=ro"))
	testutil.FailErr(t, "open page ownership reader", err)
	defer reader.Close()
	for _, table := range []string{"first", "second"} {
		testutil.FailErr(t, "partial check cannot detect cross-table alias", scanCheck(t.Context(), reader, `PRAGMA integrity_check("`+table+`")`))
	}
	var damage *StoreIncompatibleError
	if err := auditSnapshot(t.Context(), reader, nil); !errors.As(err, &damage) || damage.Reason != RecoveryReasonIntegrityFailed {
		t.Fatalf("whole-file page alias diagnosis = %v", err)
	}
}

func TestAuditUsesOneSnapshotForBothChecks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "same-view.db")
	writer, err := sql.Open("sqlite", path)
	testutil.FailErr(t, "open writer", err)
	defer writer.Close()
	_, err = writer.Exec(`PRAGMA journal_mode=WAL; CREATE TABLE parent(id INTEGER PRIMARY KEY); CREATE TABLE child(parent_id INTEGER REFERENCES parent(id))`)
	testutil.FailErr(t, "seed consistent view", err)
	reader, err := sql.Open("sqlite", fileDSN(path, "mode=ro"))
	testutil.FailErr(t, "open audit reader", err)
	defer reader.Close()
	testutil.FailErr(t, "check single consistent snapshot", auditSnapshot(t.Context(), reader, func() {
		_, err := writer.Exec(`INSERT INTO child VALUES(123)`)
		testutil.FailErr(t, "commit later violation", err)
	}))
	var damage *StoreIncompatibleError
	if err := auditSnapshot(t.Context(), reader, nil); !errors.As(err, &damage) {
		t.Fatalf("later snapshot missed committed violation: %v", err)
	}
}

func TestCleanAuditPreservesConfirmedDamageStamp(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	const damage = "confirmed damage after snapshot"
	_, err = store.writer.ExecContext(t.Context(), `INSERT INTO store_meta(key,value) VALUES(?,?)`, integrityAuditFailedMetaKey, damage)
	if err != nil {
		t.Fatalf("stamp confirmed damage: %v", err)
	}
	if err := clearIntegrityAuditFailed(t.Context(), store.writer); err != nil {
		t.Fatalf("clear pending audit: %v", err)
	}
	state, err := integrityAuditState(t.Context(), store.writer)
	if err != nil {
		t.Fatalf("read audit state: %v", err)
	}
	if state != damage {
		t.Fatalf("audit state = %q; want confirmed damage preserved", state)
	}
	diagnosed := &StoreIncompatibleError{Reason: RecoveryReasonIntegrityFailed, Detail: damage}
	store.health.failure.Store(diagnosed)
	if err := finishIntegrityAudit(t.Context(), store, nil); !errors.Is(err, diagnosed) {
		t.Fatalf("successful snapshot masked concurrent diagnosis: %v", err)
	}
}

// A completed checkpoint allows frame reuse even while a new mark-zero reader
// pins subsequent commits. File length cannot measure that retained work.
func TestAuditPressureCountsReusedWALFrames(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reuse.db")
	writer, err := sql.Open("sqlite", path)
	testutil.FailErr(t, "open WAL writer", err)
	writer.SetMaxOpenConns(1)
	defer writer.Close()
	_, err = writer.Exec(`PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0; PRAGMA journal_size_limit=67108864; CREATE TABLE payload(value BLOB); INSERT INTO payload VALUES(zeroblob(8388608))`)
	testutil.FailErr(t, "allocate reusable WAL", err)
	result, err := checkpointDatabase(t.Context(), writer, WALCheckpointPassive)
	testutil.FailErr(t, "backfill initial WAL", err)
	if !result.Complete() {
		t.Fatal("initial WAL not fully backfilled")
	}
	before, err := os.Stat(path + "-wal")
	testutil.FailErr(t, "measure allocated WAL", err)
	reader, err := sql.Open("sqlite", fileDSN(path, "mode=ro"))
	testutil.FailErr(t, "open snapshot reader", err)
	defer reader.Close()
	tx, err := reader.BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
	testutil.FailErr(t, "begin mark-zero snapshot", err)
	defer tx.Rollback()
	var count int
	testutil.FailErr(t, "establish checkpointed snapshot", tx.QueryRowContext(t.Context(), `SELECT count(*) FROM payload`).Scan(&count))
	store := newStore(writer, reader)
	auditCtx, finish := store.audit.begin(t.Context(), auditPinnedFrameLimit)
	defer finish()
	_, err = writer.Exec(`INSERT INTO payload VALUES(zeroblob(5242880))`)
	testutil.FailErr(t, "reuse WAL allocation", err)
	after, err := os.Stat(path + "-wal")
	testutil.FailErr(t, "measure reused WAL", err)
	if after.Size() != before.Size() {
		t.Fatalf("fixture WAL grew: %d -> %d", before.Size(), after.Size())
	}
	result, err = CheckpointWAL(t.Context(), store)
	testutil.FailErr(t, "observe pinned reused frames", err)
	if result.PinnedFrames() < auditPinnedFrameLimit {
		t.Fatalf("fixture pinned %d frames", result.PinnedFrames())
	}
	if !errors.Is(context.Cause(auditCtx), errAuditDeferred) {
		t.Fatalf("reused frames did not release audit: %v", context.Cause(auditCtx))
	}
	testutil.FailErr(t, "release only snapshot", tx.Rollback())
	result, err = checkpointDatabase(t.Context(), writer, WALCheckpointPassive)
	testutil.FailErr(t, "backfill reused frames", err)
	if !result.Complete() {
		t.Fatal("released snapshot still pins reused frames")
	}
}
