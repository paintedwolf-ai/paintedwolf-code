package db

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestWorkTallyCountsStatementsRowsAndPeaksOfItsContextOnly(t *testing.T) {
	database, err := openCounted("file:" + filepath.Join(t.TempDir(), "work.db"))
	testutil.FailErr(t, "open counted store", err)
	defer func() { _ = database.Close() }()
	database.SetMaxOpenConns(1)
	ctx := t.Context()
	_, err = database.ExecContext(ctx, `PRAGMA foreign_keys = ON;
CREATE TABLE parent (id INTEGER PRIMARY KEY);
CREATE TABLE child (id INTEGER PRIMARY KEY, parent INTEGER REFERENCES parent(id) ON DELETE CASCADE);
CREATE TABLE audit (n INTEGER);
CREATE TRIGGER child_audit AFTER DELETE ON child BEGIN INSERT INTO audit VALUES (OLD.id); END;`)
	testutil.FailErr(t, "create schema", err)

	var seed WorkTally
	seeded := WithWorkTally(ctx, &seed)
	tx, err := database.BeginTx(seeded, nil)
	testutil.FailErr(t, "begin", err)
	for _, statement := range []string{
		`INSERT INTO parent (id) VALUES (1), (2)`,
		`INSERT INTO child (parent) VALUES (1), (1), (1), (2)`,
	} {
		_, err := tx.ExecContext(seeded, statement)
		testutil.FailErr(t, "seed", err)
	}
	testutil.FailErr(t, "commit", tx.Commit())
	if got := seed.Work(); got.Statements != 2 || got.Transactions != 1 || got.RowsChanged != 6 || got.PeakTransactionRowsChanged != 6 || got.PeakStatementRowsChanged != 4 {
		t.Fatalf("seed work = %+v", got)
	}

	var remove WorkTally
	removing := WithWorkTally(ctx, &remove)
	// One parent row cascades three child rows, each auditing one row.
	_, err = database.ExecContext(removing, `DELETE FROM parent WHERE id = 1`)
	testutil.FailErr(t, "delete parent", err)
	rows, err := database.QueryContext(removing, `SELECT n FROM audit`)
	testutil.FailErr(t, "read audit", err)
	audited := 0
	for rows.Next() {
		audited++
	}
	testutil.FailErr(t, "close audit", rows.Close())
	if audited != 3 {
		t.Fatalf("audit rows = %d, want 3", audited)
	}
	got := remove.Work()
	if got.Statements != 2 || got.RowsChanged != 7 || got.PeakStatementRowsChanged != 7 || got.PeakTransactionRowsChanged != 7 || got.Transactions != 1 {
		t.Fatalf("delete work = %+v", got)
	}
	if got.RowsReturned != 3 || got.PeakStatementRowsReturned != 3 || got.PagesFetched <= 0 {
		t.Fatalf("read work = %+v", got)
	}
	if seed.Work().Statements != 2 {
		t.Fatalf("another context's statements reached the seed tally: %+v", seed.Work())
	}
	if process := ProcessWork(); process.Statements < 5 || process.RowsChanged < 13 {
		t.Fatalf("process work = %+v", process)
	}
}

func TestVacuumPassPagesNeverExceedsThePassLimit(t *testing.T) {
	for _, freelist := range []int64{256, 65_536, 1_000_000, 9_100_000} {
		pages := VacuumPassPages(freelist, 4096)
		if pages < vacuumSlicesPerPass || pages > VacuumPassPageLimit {
			t.Fatalf("VacuumPassPages(%d) = %d, outside %d..%d", freelist, pages, vacuumSlicesPerPass, VacuumPassPageLimit)
		}
	}
	if VacuumPassPages(1_000_000, 4096) > VacuumPassPages(100_000, 4096) {
		t.Fatal("a longer freelist gave a larger pass")
	}
	// A 4 KiB page's pointer map covers 819 pages, so a full pass spans at most four.
	if got := VacuumPassShrinkLimit(4096); got != VacuumPassPageLimit+4 {
		t.Fatalf("VacuumPassShrinkLimit(4096) = %d", got)
	}
}
