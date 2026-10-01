package sourcecatalog

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/testutil"
)

func rangeItem(key string, weight int64) pagedview.RangeItem[TreeItem] {
	return pagedview.RangeItem[TreeItem]{Key: key, Value: TreeItem{Path: key}, Weight: weight}
}

func TestRangePagesCommitAndRollback(t *testing.T) {
	ctx := context.Background()
	db, err := openTreeDB(ctx, filepath.Join(t.TempDir(), "ranges.db"))
	testutil.FailErr(t, "open index", err)
	defer func() { _ = db.Close() }()
	_, err = db.ExecContext(ctx, rangeSchema)
	testutil.FailErr(t, "create schema", err)

	tx, err := db.BeginTx(ctx, nil)
	testutil.FailErr(t, "begin publication", err)
	pages := newRangeWriter(tx, pagedview.NewCache[uint64, pagedview.RangePage[TreeItem]](128, 2<<20))
	index, _, err := pages.Open(ctx, "view", "directory")
	testutil.FailErr(t, "open ranges", err)
	for i := range 500 {
		testutil.FailErr(t, "index directory", index.Set(ctx, rangeItem(fmt.Sprintf("%05d", i), 100)))
	}
	testutil.FailErr(t, "save root", pages.Save(ctx, "view", "directory", index, DirectoryState{Listed: true, Complete: true}))
	testutil.FailErr(t, "commit publication", tx.Commit())

	tx, err = db.BeginTx(ctx, nil)
	testutil.FailErr(t, "begin canceled update", err)
	pages = newRangeWriter(tx, nil)
	index, _, err = pages.Open(ctx, "view", "directory")
	testutil.FailErr(t, "reopen ranges", err)
	testutil.FailErr(t, "change weight", index.Set(ctx, rangeItem("00000", 1)))
	testutil.FailErr(t, "save canceled root", pages.Save(ctx, "view", "directory", index, DirectoryState{Listed: true}))
	testutil.FailErr(t, "rollback publication", tx.Rollback())

	index, state, err := newRangeReader(db, nil).Open(ctx, "view", "directory")
	testutil.FailErr(t, "read committed root", err)
	item, offset, err := index.Select(ctx, 49999)
	testutil.FailErr(t, "select last row", err)
	if item.Value.Path != "00499" || offset != 99 || !state.Complete {
		t.Fatalf("rollback changed visible coordinates: %+v %d %+v", item, offset, state)
	}
}

func TestRangePagesKeepProjectionScopesIndependent(t *testing.T) {
	ctx := context.Background()
	db, err := openTreeDB(ctx, filepath.Join(t.TempDir(), "ranges.db"))
	testutil.FailErr(t, "open index", err)
	defer func() { _ = db.Close() }()
	_, err = db.ExecContext(ctx, rangeSchema)
	testutil.FailErr(t, "create schema", err)
	for _, scope := range []string{"review-a", "review-b"} {
		tx, beginErr := db.BeginTx(ctx, nil)
		testutil.FailErr(t, "begin scoped publication", beginErr)
		pages := newRangeWriter(tx, nil)
		index, _, openErr := pages.Open(ctx, scope, ".")
		testutil.FailErr(t, "open scoped root", openErr)
		testutil.FailErr(t, "write scoped row", index.Set(ctx, rangeItem(scope, 1)))
		testutil.FailErr(t, "save scoped root", pages.Save(ctx, scope, ".", index, DirectoryState{Listed: true, Complete: true}))
		testutil.FailErr(t, "commit scoped publication", tx.Commit())
	}
	if _, err := db.ExecContext(ctx, "DELETE FROM range_pages WHERE scope=?", "review-a"); err != nil {
		t.Fatalf("delete first scope pages: %v", err)
	}
	if _, err := db.ExecContext(ctx, "DELETE FROM range_roots WHERE scope=?", "review-a"); err != nil {
		t.Fatalf("delete first scope roots: %v", err)
	}
	index, state, err := newRangeReader(db, nil).Open(ctx, "review-b", ".")
	testutil.FailErr(t, "open surviving scope", err)
	item, err := index.SelectItem(ctx, 0)
	testutil.FailErr(t, "read surviving scope", err)
	if item.Value.Path != "review-b" || !state.Complete {
		t.Fatalf("surviving scope item=%+v state=%+v", item, state)
	}
}
