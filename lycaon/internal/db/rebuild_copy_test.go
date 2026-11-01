package db

import (
	"bytes"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

func TestRetainedCopyPreservesReleasedRowsAndHistory(t *testing.T) {
	source, _ := seedLegacyManifestStore(t, t.TempDir(), map[string]int{"/work/a": 20, "/work/b": 3})
	reader, err := OpenReadOnly(t.Context(), source)
	testutil.FailErr(t, "open source", err)
	defer func() { _ = reader.Close() }()
	plan, err := PlanUpgrade(t.Context(), reader)
	testutil.FailErr(t, "plan upgrade", err)
	destination := filepath.Join(t.TempDir(), "rebuilt.db")
	_, err = copyRetainedStore(t.Context(), source, destination, plan, nil)
	testutil.FailErr(t, "copy retained store", err)
	copyDB, err := sql.Open("sqlite", destination)
	testutil.FailErr(t, "open retained copy", err)
	defer func() { _ = copyDB.Close() }()
	testutil.FailErr(t, "validate retained store", auditSnapshot(t.Context(), copyDB, nil))
	_, err = copyDB.Exec(`ATTACH DATABASE ? AS original`, fileDSN(source, "mode=ro"))
	testutil.FailErr(t, "attach reference", err)
	objects, err := readRebuildObjects(t.Context(), copyDB)
	testutil.FailErr(t, "read source tables", err)
	state, err := integrityAuditState(t.Context(), copyDB)
	testutil.FailErr(t, "read reconstructed audit obligation", err)
	if state != integrityAuditPendingValue {
		t.Fatal("reconstructed image lost its deferred audit obligation")
	}
	for _, object := range objects {
		if object.kind != "table" || object.tableKind == "virtual" || object.name == "source_blob_reclaim_queue" || plan.CopyPlan().Sources[object.name] != "" {
			continue
		}
		t.Run(object.name, func(t *testing.T) {
			name := rebuildIdentifier(object.name)
			predicate := ""
			if object.name == "store_meta" {
				predicate = " WHERE key <> 'integrity_audit_failed'"
			}
			for _, pair := range [][2]string{{"main", "original"}, {"original", "main"}} {
				var different int
				query := "SELECT COUNT(*) FROM (SELECT * FROM " + pair[0] + "." + name + predicate + " EXCEPT SELECT * FROM " + pair[1] + "." + name + predicate + ")"
				testutil.FailErr(t, "compare unchanged rows", copyDB.QueryRow(query).Scan(&different))
				if different != 0 {
					t.Fatalf("%d unchanged rows differ", different)
				}
			}
		})
	}
	var discarded int
	testutil.FailErr(t, "inspect discarded chunks", copyDB.QueryRow(`SELECT COUNT(*) FROM source_manifest_chunks WHERE id='orphan'`).Scan(&discarded))
	if discarded != 0 {
		t.Fatal("retained copy visited unreferenced chunks")
	}
	testutil.FailErr(t, "close copy before upgrade", copyDB.Close())
	testutil.FailErr(t, "upgrade retained copy", UpgradeStaged(t.Context(), destination))
	upgraded, err := sql.Open("sqlite", destination)
	testutil.FailErr(t, "open upgraded copy", err)
	defer func() { _ = upgraded.Close() }()
	var count int
	testutil.FailErr(t, "count retained snapshot entries", upgraded.QueryRow(`SELECT SUM(m.entry_count)
FROM source_snapshot_roots r JOIN source_manifests m ON m.id=r.manifest_id WHERE r.snapshot_id IN ('snapshot-one','snapshot-two')`).Scan(&count))
	if count != 43 {
		t.Fatalf("retained entries = %d, want 43", count)
	}
}

func TestRebuildGrowthLimitIsTemporaryAndRollsBackOversizedWrites(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	testutil.FailErr(t, "open bounded copy fixture", err)
	database.SetMaxOpenConns(1)
	defer func() { _ = database.Close() }()
	_, err = database.Exec(`CREATE TABLE retained(value BLOB); INSERT INTO retained VALUES('kept')`)
	testutil.FailErr(t, "seed retained row", err)
	restore, err := setRebuildPageLimit(t.Context(), database, 64<<10)
	testutil.FailErr(t, "limit unpublished growth", err)
	_, err = database.Exec(`INSERT INTO retained VALUES(zeroblob(262144))`)
	var driver *sqlite.Error
	if !errors.As(err, &driver) || driver.Code()&0xff != sqlite3.SQLITE_FULL {
		t.Fatalf("oversized copy write = %v", err)
	}
	var count int
	testutil.FailErr(t, "read rollback state", database.QueryRow(`SELECT COUNT(*) FROM retained`).Scan(&count))
	if count != 1 {
		t.Fatalf("failed allocation left %d rows", count)
	}
	testutil.FailErr(t, "remove unpublished growth limit", restore())
	_, err = database.Exec(`INSERT INTO retained VALUES(zeroblob(262144))`)
	testutil.FailErr(t, "allow ordinary growth after publication", err)
}

func TestRetainedCopyDoesNotReadRetiredIndex(t *testing.T) {
	source, _ := seedLegacyManifestStore(t, t.TempDir(), map[string]int{"/work/a": 20, "/work/b": 3})
	reader, err := sql.Open("sqlite", source)
	testutil.FailErr(t, "open fixture", err)
	plan, err := PlanUpgrade(t.Context(), reader)
	testutil.FailErr(t, "plan fixture upgrade", err)
	var page, pageSize int64
	testutil.FailErr(t, "locate retired index", reader.QueryRow(`SELECT rootpage FROM sqlite_schema WHERE name='idx_source_manifest_entries_path'`).Scan(&page))
	testutil.FailErr(t, "read page size", reader.QueryRow(`PRAGMA page_size`).Scan(&pageSize))
	testutil.FailErr(t, "close fixture", reader.Close())
	file, err := os.OpenFile(source, os.O_RDWR, 0)
	testutil.FailErr(t, "open fixture bytes", err)
	_, err = file.WriteAt(make([]byte, pageSize), (page-1)*pageSize)
	testutil.FailErr(t, "damage obsolete index", err)
	testutil.FailErr(t, "close fixture bytes", file.Close())
	reader, err = OpenReadOnly(t.Context(), source)
	testutil.FailErr(t, "open damaged source", err)
	if err := auditSnapshot(t.Context(), reader, nil); err == nil {
		t.Fatal("fixture did not damage the obsolete index")
	}
	plan, err = plan.PrepareRetainedStorage(t.Context(), reader)
	testutil.FailErr(t, "measure primary records despite obsolete index damage", err)
	if !plan.BoundedRetainedStorage() {
		t.Fatal("damaged obsolete index lost reviewed resource proof")
	}
	testutil.FailErr(t, "close damaged source", reader.Close())
	destination := filepath.Join(t.TempDir(), "rebuilt.db")
	_, err = copyRetainedStore(t.Context(), source, destination, plan, nil)
	testutil.FailErr(t, "copy without obsolete index reads", err)
	testutil.FailErr(t, "upgrade retained data", UpgradeStaged(t.Context(), destination))
}

func TestRetainedCopyRefusesExistingDestination(t *testing.T) {
	source, _ := seedLegacyManifestStore(t, t.TempDir(), map[string]int{"/work/a": 1, "/work/b": 1})
	reader, err := OpenReadOnly(t.Context(), source)
	testutil.FailErr(t, "open source", err)
	defer func() { _ = reader.Close() }()
	plan, err := PlanUpgrade(t.Context(), reader)
	testutil.FailErr(t, "plan upgrade", err)
	if _, err := copyRetainedStore(t.Context(), source, source, plan, nil); !os.IsExist(err) {
		t.Fatalf("existing target error = %v", err)
	}
	actual, err := inspectSchema(t.Context(), reader)
	testutil.FailErr(t, "inspect unchanged source", err)
	if actual != plan.Source {
		t.Fatal("refused destination changed")
	}
}

func TestRetainedCopyReadsPrimaryRowsWhenCoveringIndexIsDamaged(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source.db")
	database, err := sql.Open("sqlite", source)
	testutil.FailErr(t, "open source", err)
	_, err = database.Exec(`CREATE TABLE retained(value TEXT); CREATE INDEX retained_values ON retained(value); INSERT INTO retained VALUES('first'),('second')`)
	testutil.FailErr(t, "seed retained rows and covering index", err)
	var page, pageSize int64
	testutil.FailErr(t, "locate covering index", database.QueryRow(`SELECT rootpage FROM sqlite_schema WHERE name='retained_values'`).Scan(&page))
	testutil.FailErr(t, "read source page size", database.QueryRow(`PRAGMA page_size`).Scan(&pageSize))
	testutil.FailErr(t, "close source", database.Close())
	file, err := os.OpenFile(source, os.O_RDWR, 0)
	testutil.FailErr(t, "open index bytes", err)
	_, err = file.WriteAt(make([]byte, pageSize), (page-1)*pageSize)
	testutil.FailErr(t, "damage covering index", err)
	testutil.FailErr(t, "close index bytes", file.Close())
	destination, err := sql.Open("sqlite", ":memory:")
	testutil.FailErr(t, "open replacement", err)
	defer destination.Close()
	_, err = destination.Exec(`CREATE TABLE retained(value TEXT); ATTACH DATABASE ? AS original`, fileDSN(source, "mode=ro"))
	testutil.FailErr(t, "attach damaged source", err)
	tx, err := destination.BeginTx(t.Context(), nil)
	testutil.FailErr(t, "begin retained copy", err)
	defer tx.Rollback()
	testutil.FailErr(t, "copy primary rows", copyRebuildTable(t.Context(), tx, rebuildObject{name: "retained"}, ""))
	var count int
	testutil.FailErr(t, "verify preserved rows", tx.QueryRow(`SELECT COUNT(*) FROM main.retained WHERE (rowid=1 AND value='first') OR (rowid=2 AND value='second')`).Scan(&count))
	if count != 2 {
		t.Fatalf("retained rows copied=%d", count)
	}
}

func TestRetainedCopyDiskFullPreservesSource(t *testing.T) {
	source, _ := seedLegacyManifestStore(t, t.TempDir(), map[string]int{"/work/a": 20})
	reader, err := OpenReadOnly(t.Context(), source)
	testutil.FailErr(t, "open source", err)
	plan, err := PlanUpgrade(t.Context(), reader)
	testutil.FailErr(t, "plan retained copy", err)
	testutil.FailErr(t, "close source", reader.Close())
	before, err := os.ReadFile(source)
	testutil.FailErr(t, "read source bytes", err)
	copyDB, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "copy.db"))
	testutil.FailErr(t, "open bounded copy", err)
	defer copyDB.Close()
	copyDB.SetMaxOpenConns(1)
	_, err = copyDB.Exec(`PRAGMA max_page_count=8`)
	testutil.FailErr(t, "bound replacement pages", err)
	err = classifyUpgradeFailure(copyRetainedDatabase(t.Context(), copyDB, source, plan), plan.Source.Revision)
	var full *sqlite.Error
	if !errors.As(err, &full) || full.Code()&0xff != sqlite3.SQLITE_FULL || errors.Is(err, ErrStoreIncompatible) {
		t.Fatalf("copy allocation failure lost its operational cause: %v", err)
	}
	var objects int
	testutil.FailErr(t, "inspect rolled back replacement", copyDB.QueryRow(`SELECT COUNT(*) FROM main.sqlite_schema`).Scan(&objects))
	if objects != 0 {
		t.Fatalf("failed copy retained %d schema objects", objects)
	}
	after, err := os.ReadFile(source)
	testutil.FailErr(t, "read original after failed copy", err)
	if !bytes.Equal(before, after) {
		t.Fatal("disk-full copy changed original bytes")
	}
}
