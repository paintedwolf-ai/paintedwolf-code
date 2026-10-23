package db

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// The background audit reads one table at a time, so writes during it are
// checkpointed between tables instead of piling up behind one snapshot.
func TestAuditLetsTheWALCheckpointBetweenTables(t *testing.T) {
	store, _ := openTestDBWithPath(t)
	audited, complete := 0, 0
	store.auditedTable = func(table string) {
		audited++
		growWAL(t, store, "audit-"+table, 20)
		result, err := CheckpointWAL(t.Context(), store)
		testutil.FailErr(t, "checkpoint during audit", err)
		if result.Complete() {
			complete++
		}
	}
	testutil.FailErr(t, "audit store", AuditIntegrity(t.Context(), store))
	if audited < 10 {
		t.Fatalf("audit visited %d tables, want every table", audited)
	}
	if complete != audited {
		t.Fatalf("%d of %d checkpoints during the audit were pinned by it", audited-complete, audited)
	}
}

func TestAuditRetriesOnlyUnfinishedTablesWithoutADeadline(t *testing.T) {
	_, store := uncleanAuditStore(t)
	tables, err := auditableTables(t.Context(), store.reader)
	testutil.FailErr(t, "list audit tables", err)
	attempts := make(map[string]int)
	waits := 0
	testutil.FailErr(t, "finish deferred audit", auditTables(t.Context(), store.reader, nil,
		func(ctx context.Context, table string) error {
			attempts[table]++
			if table == tables[0] && attempts[table] <= 3 {
				return errAuditDeferred
			}
			return auditTable(ctx, store.reader, table)
		}, func(ctx context.Context) error {
			waits++
			state, err := integrityAuditState(ctx, store.reader)
			testutil.FailErr(t, "read pending audit", err)
			if state != integrityAuditPendingValue || store.Failure() != nil {
				t.Fatal("unfinished audit lost its obligation or quarantined the store")
			}
			return nil
		}))
	if waits != 3 || attempts[tables[0]] != 4 {
		t.Fatalf("retry progress: waits=%d attempts=%d", waits, attempts[tables[0]])
	}
	for _, table := range tables[1:] {
		if attempts[table] != 1 {
			t.Fatalf("completed table %s repeated %d times", table, attempts[table])
		}
	}
	testutil.FailErr(t, "complete production audit", AuditIntegrity(t.Context(), store))
	state, err := integrityAuditState(t.Context(), store.reader)
	testutil.FailErr(t, "read completed state", err)
	if state != "" {
		t.Fatalf("completed audit state = %q", state)
	}
}

func TestAuditCheckpointPressureReleasesOnlyItsRead(t *testing.T) {
	_, store := uncleanAuditStore(t)
	ctx, finish := store.audit.begin(t.Context())
	defer finish()
	if _, limited := ctx.Deadline(); limited {
		t.Fatal("audit imposed a machine-speed deadline")
	}
	store.audit.checkpoint(WALCheckpoint{LogFrames: auditPinnedFrameLimit - 1})
	if ctx.Err() != nil {
		t.Fatal("audit yielded below its pinned-frame budget")
	}
	store.audit.checkpoint(WALCheckpoint{LogFrames: auditPinnedFrameLimit})
	if !errors.Is(context.Cause(ctx), errAuditDeferred) {
		t.Fatalf("audit pressure cause = %v", context.Cause(ctx))
	}
	if store.Failure() != nil || t.Context().Err() != nil {
		t.Fatal("audit pressure affected application health or caller context")
	}
	finish()
	next, release := store.audit.begin(t.Context())
	defer release()
	if next.Err() != nil {
		t.Fatal("previous pressure prevented the next read")
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := waitForAuditCapacity(canceled, store); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel idle wait = %v", err)
	}
	if err := auditTable(canceled, store.reader, "store_meta"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel audit = %v", err)
	}
}

func TestRealCheckpointPressureInterruptsAuditAndThenCompletes(t *testing.T) {
	_, store := uncleanAuditStore(t)
	ctx, finish := store.audit.begin(t.Context())
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

func TestAuditCannotClearBeforeWholeFileCoverage(t *testing.T) {
	_, store := uncleanAuditStore(t)
	globalAttempts, waits := 0, 0
	err := auditTables(t.Context(), store.reader, nil, func(ctx context.Context, table string) error {
		if table == "" {
			globalAttempts++
			if globalAttempts == 1 {
				return errAuditDeferred
			}
		}
		return auditTable(ctx, store.reader, table)
	}, func(ctx context.Context) error {
		waits++
		state, err := integrityAuditState(ctx, store.reader)
		testutil.FailErr(t, "read outstanding global obligation", err)
		if state != integrityAuditPendingValue {
			t.Fatal("partial coverage cleared audit obligation")
		}
		return nil
	})
	testutil.FailErr(t, "complete whole-file audit", err)
	if globalAttempts != 2 || waits != 1 {
		t.Fatalf("global attempts=%d waits=%d", globalAttempts, waits)
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
		testutil.FailErr(t, "partial check cannot detect cross-table alias", auditTable(t.Context(), reader, table))
	}
	var damage *StoreIncompatibleError
	if err := auditTable(t.Context(), reader, ""); !errors.As(err, &damage) || damage.Reason != RecoveryReasonIntegrityFailed {
		t.Fatalf("whole-file page alias diagnosis = %v", err)
	}
}
