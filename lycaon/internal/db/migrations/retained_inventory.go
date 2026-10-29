package migrations

import (
	"context"
	"errors"
	"fmt"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

type retainedTree struct {
	name, kind, table string
	cells, record     uint64
}

// Inspect primary trees before indexes so an unreadable obsolete index can
// fall back to its complete primary records. dbstat aggregate mode suppresses
// soft corruption, so retain and check every page's structured type instead.
func retainedInventory(ctx context.Context, database DBTX) (cells, indexRecord, manifestRecord, manifestCells uint64, err error) {
	rows, err := database.QueryContext(ctx, `SELECT name,type,tbl_name FROM sqlite_schema WHERE rootpage>0 AND type IN ('table','index') ORDER BY type DESC`)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	var trees []retainedTree
	for rows.Next() {
		var tree retainedTree
		if err = rows.Scan(&tree.name, &tree.kind, &tree.table); err != nil {
			break
		}
		trees = append(trees, tree)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return 0, 0, 0, 0, err
	}
	primary := map[string]uint64{}
	for _, tree := range trees {
		var bad uint64
		scanErr := database.QueryRowContext(ctx, `SELECT COALESCE(SUM(ncell),0),COALESCE(MAX(mx_payload),0),
 COALESCE(SUM(CASE WHEN pagetype IS NULL OR pagetype NOT IN ('internal','leaf','overflow') THEN 1 ELSE 0 END),0)
 FROM dbstat WHERE name=?`, tree.name).Scan(&tree.cells, &tree.record, &bad)
		if scanErr != nil {
			var failure *sqlite.Error
			if tree.kind != "index" || !errors.As(scanErr, &failure) || failure.Code()&0xff != sqlite3.SQLITE_CORRUPT {
				return 0, 0, 0, 0, scanErr
			}
			bad = 1
		}
		if tree.kind == "table" {
			if bad != 0 {
				return 0, 0, 0, 0, fmt.Errorf("%w: retained primary tree %s is corrupt", ErrIntegrity, tree.name)
			}
			primary[tree.name] = tree.record
			cells, err = storageSum(cells, tree.cells)
			if err != nil {
				return 0, 0, 0, 0, err
			}
			if tree.name == "source_manifest_entries" {
				manifestRecord, manifestCells = tree.record, tree.cells
			}
			if tree.name == "evidence_records" || tree.name == "source_snapshot_roots" {
				indexRecord = max(indexRecord, tree.record)
			}
		} else {
			if bad != 0 {
				tree.record = max(tree.record, primary[tree.table])
			}
			indexRecord = max(indexRecord, tree.record)
		}
	}
	return cells, indexRecord, manifestRecord, manifestCells, nil
}
