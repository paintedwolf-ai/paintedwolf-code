package db

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// openFreelistStore builds a store of small pages holding more than one pass of
// free pages, the shape of a store after large collection.
func openFreelistStore(t *testing.T, freeRows int) *Store {
	t.Helper()
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "reclaim.db")
	writer, err := sql.Open("sqlite", fileDSN(path, writerDSNQuery))
	testutil.FailErr(t, "open fixture writer", err)
	t.Cleanup(func() { _ = writer.Close() })
	writer.SetMaxOpenConns(writerOpenConns)
	_, err = writer.ExecContext(ctx, `PRAGMA page_size=512; PRAGMA auto_vacuum=INCREMENTAL; PRAGMA journal_mode=WAL; PRAGMA synchronous=NORMAL`)
	testutil.FailErr(t, "configure fixture", err)
	testutil.FailErr(t, "create fixture schema", prepareStoreSchema(ctx, writer, true))
	reader, err := openReader(ctx, path)
	testutil.FailErr(t, "open fixture reader", err)
	store := newStore(writer, reader)
	t.Cleanup(func() { _ = store.Close() })
	<-store.maintenanceSignals()
	freeScratchPages(t, store, "first_free", freeRows)
	return store
}

func freeScratchPages(t *testing.T, store Handle, table string, rows int) {
	t.Helper()
	ctx := t.Context()
	_, err := store.ExecContext(ctx, `CREATE TABLE `+table+`(body BLOB NOT NULL)`)
	testutil.FailErr(t, "create scratch table", err)
	_, err = store.ExecContext(ctx, `WITH RECURSIVE r(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM r WHERE n<?)
 INSERT INTO `+table+` SELECT zeroblob(400) FROM r`, rows)
	testutil.FailErr(t, "fill scratch table", err)
	_, err = store.ExecContext(ctx, `DROP TABLE `+table)
	testutil.FailErr(t, "free scratch pages", err)
}

func pragmaPages(t *testing.T, store Handle, pragma string) int64 {
	t.Helper()
	value, err := readPragmaInt(t.Context(), store, pragma)
	testutil.FailErr(t, "read "+pragma, err)
	return value
}

func freeTurns(context.Context, *Store) error { return nil }

// The row-retention scans run once per wake, not once per vacuum pass.
func TestDrainReturnsPagesWithOneRowRoundPerWake_AC_DB_03(t *testing.T) {
	store := openFreelistStore(t, 6000)
	cfg := DefaultRetention()
	before := pragmaPages(t, store, "page_count")
	testutil.FailErr(t, "drain", drainMaintenance(t.Context(), store, cfg, freeTurns))
	progress, err := store.MaintenanceProgress(t.Context(), cfg)
	testutil.FailErr(t, "read progress", err)
	if progress.Passes < 3 {
		t.Fatalf("passes = %d, fixture too small", progress.Passes)
	}
	if progress.RowRounds != 1 {
		t.Fatalf("row rounds = %d over %d passes", progress.RowRounds, progress.Passes)
	}
	if got := before - progress.PageCount; got != progress.PagesReclaimed || got == 0 {
		t.Fatalf("file shrank %d pages, reported %d", got, progress.PagesReclaimed)
	}
	if progress.ReclaimedBytes != progress.PagesReclaimed*progress.PageSize {
		t.Fatalf("reclaimed bytes = %d", progress.ReclaimedBytes)
	}
	if progress.DatabaseBytes == 0 || progress.UsedBytes+progress.FreeBytes != progress.PageCount*progress.PageSize {
		t.Fatalf("byte accounting = %+v", progress)
	}
	if progress.FreelistPages >= cfg.IncrementalVacuumMinFreelistPages || progress.Condition != ReclaimBelowThreshold {
		t.Fatalf("progress = %+v, want a drained freelist", progress)
	}
	if progress.Checkpoints != progress.Passes-1 {
		t.Fatalf("checkpoints = %d over %d passes", progress.Checkpoints, progress.Passes)
	}
}

func TestDrainRunsRowRetentionAgainForAWriteWake_AC_DB_03(t *testing.T) {
	store := openFreelistStore(t, 6000)
	signaled := false
	turn := func(context.Context, *Store) error {
		if !signaled {
			signaled = true
			return nil
		}
		store.signalMaintenance()
		return nil
	}
	cfg := DefaultRetention()
	testutil.FailErr(t, "drain", drainMaintenance(t.Context(), store, cfg, turn))
	progress, err := store.MaintenanceProgress(t.Context(), cfg)
	testutil.FailErr(t, "read progress", err)
	if progress.RowRounds < 2 {
		t.Fatalf("row rounds = %d after write wakes", progress.RowRounds)
	}
}

// freeingVacuum drops a populated table after every slice, as a collection
// transaction running beside the vacuum would.
type freeingVacuum struct {
	*Store
	t      *testing.T
	tables *int
}

func (d freeingVacuum) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	result, err := d.Store.ExecContext(ctx, query, args...)
	_, dropErr := d.Store.ExecContext(ctx, fmt.Sprintf(`DROP TABLE foreground_%d`, *d.tables))
	testutil.FailErr(d.t, "free foreground pages", dropErr)
	*d.tables++
	return result, err
}

// A foreground burst that frees more than a slice reclaims must not end the wake.
func TestForegroundFreesDoNotEndAWorkingVacuum_AC_DB_03(t *testing.T) {
	store := openFreelistStore(t, 1)
	for table := range vacuumSlicesPerPass {
		_, err := store.ExecContext(t.Context(), fmt.Sprintf(`CREATE TABLE foreground_%d(body BLOB NOT NULL);
WITH RECURSIVE r(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM r WHERE n<1000) INSERT INTO foreground_%d SELECT zeroblob(400) FROM r`, table, table))
		testutil.FailErr(t, "fill foreground table", err)
	}
	freeScratchPages(t, store, "freed_earlier", 6000)
	freelist := pragmaPages(t, store, "freelist_count")
	tables := 0
	pass, err := runIncrementalVacuum(t.Context(), freeingVacuum{Store: store, t: t, tables: &tables}, 256)
	testutil.FailErr(t, "vacuum during foreground frees", err)
	if pass.FreelistPages <= freelist {
		t.Fatalf("freelist %d -> %d: the fixture needs growth", freelist, pass.FreelistPages)
	}
	if !pass.Progressed || pass.Slices != vacuumSlicesPerPass {
		t.Fatalf("pass = %+v, want all slices", pass)
	}
}

func TestVacuumPassStaysWithinItsPageBudget_AC_DB_04(t *testing.T) {
	store := openFreelistStore(t, 6000)
	before := pragmaPages(t, store, "page_count")
	pass, err := runIncrementalVacuum(t.Context(), store, 256)
	testutil.FailErr(t, "vacuum pass", err)
	if pass.Slices != vacuumSlicesPerPass || pass.PagesRequested != vacuumSlicesPerPass*vacuumSlicePages {
		t.Fatalf("pass = %+v", pass)
	}
	// Pointer-map pages leave with the free pages they describe.
	if pass.PagesReclaimed < pass.PagesRequested || pass.PagesReclaimed > pass.PagesRequested+pass.PagesRequested/16 {
		t.Fatalf("reclaimed %d of %d requested", pass.PagesReclaimed, pass.PagesRequested)
	}
	if got := before - pragmaPages(t, store, "page_count"); got != pass.PagesReclaimed {
		t.Fatalf("file shrank %d, pass reports %d", got, pass.PagesReclaimed)
	}
}

func TestVacuumWorkFollowsTheFreelistChain_AC_DB_04(t *testing.T) {
	const pageSize = 4096
	for _, tc := range []struct {
		name        string
		free        int64
		wantPages   int64
		wantCacheAt int64
	}{
		{"short chain keeps full slices and the default cache", 100_000, vacuumSlicePages, 0},
		{"chain at the slice budget", 500_000, vacuumSlicePages, 1024},
		{"long chain", 1_000_000, 66, 2048},
		{"measured store", 9_109_377, 7, 9216},
		{"cache cap", 100_000_000, 1, vacuumCacheMaxPages},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pages := vacuumPagesForSlice(tc.free, pageSize)
			if pages != tc.wantPages {
				t.Fatalf("slice pages = %d want %d", pages, tc.wantPages)
			}
			if pages > 1 && pages*freelistTrunks(tc.free, pageSize) > vacuumSliceTrunkVisits {
				t.Fatalf("slice visits %d trunks", pages*freelistTrunks(tc.free, pageSize))
			}
			if got := vacuumCachePages(tc.free, pageSize); got != tc.wantCacheAt {
				t.Fatalf("cache pages = %d want %d", got, tc.wantCacheAt)
			}
		})
	}
}

func TestDrainReturnsTheWriterCacheToItsDefault_AC_DB_04(t *testing.T) {
	store := openFreelistStore(t, 10)
	ctx := t.Context()
	testutil.FailErr(t, "size cache", store.fitVacuumCache(ctx, 9_109_377, 4096))
	var size int64
	testutil.FailErr(t, "read cache", store.writer.QueryRowContext(ctx, `PRAGMA cache_size`).Scan(&size))
	if size != 9216 {
		t.Fatalf("vacuum cache = %d", size)
	}
	testutil.FailErr(t, "drain", drainMaintenance(ctx, store, DefaultRetention(), freeTurns))
	testutil.FailErr(t, "read cache", store.writer.QueryRowContext(ctx, `PRAGMA cache_size`).Scan(&size))
	if size != defaultWriterCacheSize {
		t.Fatalf("cache after drain = %d", size)
	}
}

// Opening a store with a large freelist neither sweeps nor rebuilds it.
func TestStartupLeavesFreePagesToMaintenance_AC_DB_03(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	store, err := Open(path)
	testutil.FailErr(t, "open store", err)
	freeScratchPages(t, store, "freed_before_restart", 3000)
	pages, free := pragmaPages(t, store, "page_count"), pragmaPages(t, store, "freelist_count")
	identity, err := os.Stat(path)
	testutil.FailErr(t, "stat store", err)
	testutil.FailErr(t, "close store", store.Close())
	reopened, err := Open(path)
	testutil.FailErr(t, "reopen store", err)
	defer func() { _ = reopened.Close() }()
	if got := pragmaPages(t, reopened, "page_count"); got != pages {
		t.Fatalf("page count %d -> %d across startup", pages, got)
	}
	if got := pragmaPages(t, reopened, "freelist_count"); got != free {
		t.Fatalf("freelist %d -> %d across startup", free, got)
	}
	current, err := os.Stat(path)
	testutil.FailErr(t, "stat reopened store", err)
	if !os.SameFile(identity, current) {
		t.Fatal("startup replaced the store file")
	}
}

func TestModeNoneStoreStaysIntactAndReportsItsPath_AC_DB_05(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "store.db")
	builder, err := sql.Open("sqlite", fileDSN(path, writerDSNQuery))
	testutil.FailErr(t, "open builder", err)
	builder.SetMaxOpenConns(1)
	_, err = builder.ExecContext(ctx, `PRAGMA auto_vacuum=NONE; CREATE TABLE fixes_vacuum_mode(v); DROP TABLE fixes_vacuum_mode`)
	testutil.FailErr(t, "fix mode none", err)
	testutil.FailErr(t, "create schema", prepareStoreSchema(ctx, builder, true))
	testutil.FailErr(t, "close builder", builder.Close())

	store, err := Open(path)
	testutil.FailErr(t, "ordinary startup", err)
	defer func() { _ = store.Close() }()
	freeScratchPages(t, store, "freed_in_mode_none", 3000)
	_, err = store.ExecContext(ctx, `INSERT INTO fts_maintenance (name,last_optimized_at) VALUES ('kept','2999-01-01T00:00:00Z')`)
	testutil.FailErr(t, "seed retained row", err)
	pages, free := pragmaPages(t, store, "page_count"), pragmaPages(t, store, "freelist_count")
	if free < DefaultRetention().IncrementalVacuumMinFreelistPages {
		t.Fatalf("freelist = %d", free)
	}
	cfg := DefaultRetention()
	testutil.FailErr(t, "idle maintenance", drainMaintenance(ctx, store, cfg, freeTurns))
	if got := pragmaPages(t, store, "page_count"); got != pages {
		t.Fatalf("page count %d -> %d", pages, got)
	}
	progress, err := store.MaintenanceProgress(ctx, cfg)
	testutil.FailErr(t, "read progress", err)
	if progress.AutoVacuumMode != 0 || progress.Condition != ReclaimRebuildRequired || progress.Action == "" {
		t.Fatalf("progress = %+v", progress)
	}
	if progress.VacuumSlices != 0 || progress.PagesRequested != 0 || progress.PagesReclaimed != 0 {
		t.Fatalf("progress = %+v, want no vacuum claimed", progress)
	}
	var kept int
	testutil.FailErr(t, "read retained row", store.QueryRowContext(ctx, `SELECT COUNT(*) FROM fts_maintenance WHERE name='kept'`).Scan(&kept))
	if kept != 1 {
		t.Fatal("maintenance lost a retained row")
	}
}

func TestProgressNamesTheActualReasonForNoReclamation_AC_DB_06(t *testing.T) {
	ctx := t.Context()
	store := openFreelistStore(t, 6000)
	enabled := DefaultRetention()
	enabled.Enabled = true
	disabled := enabled
	disabled.Enabled = false

	progress, err := store.MaintenanceProgress(ctx, disabled)
	testutil.FailErr(t, "disabled progress", err)
	if progress.Condition != ReclaimDisabled || progress.PagesReclaimed != 0 {
		t.Fatalf("disabled = %+v", progress)
	}
	free := progress.FreelistPages
	if free == 0 || progress.FreeBytes != free*progress.PageSize {
		t.Fatalf("free bytes = %+v", progress)
	}
	report, err := runMaintenancePass(ctx, store, disabled, true)
	testutil.FailErr(t, "disabled pass", err)
	if report.IncrementalVacuumed || pragmaPages(t, store, "freelist_count") != free {
		t.Fatal("disabled maintenance reclaimed pages")
	}

	progress, err = store.MaintenanceProgress(ctx, enabled)
	testutil.FailErr(t, "deferred progress", err)
	if progress.Condition != ReclaimDeferred {
		t.Fatalf("waiting pages = %+v", progress)
	}

	// Resuming makes bounded progress and does not change what is retained.
	pass, err := runIncrementalVacuum(ctx, store, enabled.IncrementalVacuumMinFreelistPages)
	testutil.FailErr(t, "resume", err)
	if pass.PagesReclaimed == 0 || pass.PagesRequested > vacuumSlicesPerPass*vacuumSlicePages {
		t.Fatalf("resumed pass = %+v", pass)
	}

	// Live pages are not free pages: a store of retained rows reports below threshold.
	testutil.FailErr(t, "drain", drainMaintenance(ctx, store, enabled, freeTurns))
	_, err = store.ExecContext(ctx, `CREATE TABLE retained_rows(body BLOB NOT NULL);
WITH RECURSIVE r(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM r WHERE n<6000) INSERT INTO retained_rows SELECT zeroblob(400) FROM r`)
	testutil.FailErr(t, "retain rows", err)
	progress, err = store.MaintenanceProgress(ctx, enabled)
	testutil.FailErr(t, "retained progress", err)
	if progress.Condition != ReclaimBelowThreshold || progress.UsedBytes <= progress.FreeBytes {
		t.Fatalf("retained store = %+v", progress)
	}
}
