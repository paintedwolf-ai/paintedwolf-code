package migrations

import (
	"context"
	"fmt"
	"math"

	sqlite3 "modernc.org/sqlite/lib"
)

func sessionModeWrites(ctx context.Context, store DBTX, _ uint64) (uint64, error) {
	var tree retainedTree
	if err := store.QueryRowContext(ctx, `SELECT COALESCE(SUM(ncell),0),COALESCE(MAX(mx_payload),0),COALESCE(SUM(pgsize),0)
FROM dbstat WHERE name='sessions'`).Scan(&tree.cells, &tree.record, &tree.bytes); err != nil {
		return 0, err
	}
	var pageBytes uint64
	if err := store.QueryRowContext(ctx, "PRAGMA page_size").Scan(&pageBytes); err != nil {
		return 0, err
	}
	if pageBytes < 512 || pageBytes > 65536 || pageBytes&(pageBytes-1) != 0 {
		return 0, fmt.Errorf("invalid session mode page size")
	}
	// Charge two table passes, the larger records and the new root index.
	row, err := storageSum(tree.record, tree.record, 4*pageBytes)
	if err != nil || tree.cells > math.MaxUint64/row {
		return 0, fmt.Errorf("session mode write estimate overflows")
	}
	return storageSum(tree.bytes, tree.bytes, tree.cells*row, 1<<20)
}

func sessionModeScratch(tree retainedTree, pageBytes uint64) (uint64, error) {
	// Lineage keys can repeat the largest root ID on every child.
	row, err := storageSum(tree.record, tree.record, 80)
	if err != nil || tree.cells > math.MaxUint64/row {
		return 0, fmt.Errorf("session mode sort input overflows")
	}
	index, err := sorterStorageBytes(tree.cells*row, tree.cells, row, pageBytes)
	if err != nil {
		return 0, err
	}
	// The recursive queue overlaps the keyed lineage table. Each can use
	// overflow pages and sparse B-tree pages in the pinned temp geometry.
	tempPage := uint64(sqlite3.SQLITE_DEFAULT_PAGE_SIZE)
	trees, err := storageSum(row, row, 4*tempPage)
	if err != nil || tree.cells > math.MaxUint64/trees {
		return 0, fmt.Errorf("session mode temporary trees overflow")
	}
	lineage, err := storageSum(tree.cells*trees, 8*tempPage)
	return max(index, lineage), err
}
