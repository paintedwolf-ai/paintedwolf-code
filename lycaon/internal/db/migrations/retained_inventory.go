package migrations

import (
	"context"
	"errors"
	"fmt"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

type retainedTree struct {
	name, kind, table    string
	cells, record, bytes uint64
}

// These primary trees supply every populated sort in the frozen route.
// Index reconstruction has its own later survey, only on the row-copy path.
func retainedRouteTree(name string) bool {
	switch name {
	case "source_manifest_chunks", "source_snapshot_chunks", "source_manifest_entries", "source_snapshot_roots", "vault_unlocks", "evidence_records", "source_files", "source_versions", "source_head_entries", "source_effects", "checkpoints", "sqlite_schema":
		return true
	}
	return false
}

// Cached source facts are immutable under the installation lease. A fallback
// surveys the remaining trees before it admits any old-index reconstruction.
func retainedInventory(ctx context.Context, database DBTX, known map[string]retainedTree, routeOnly bool) (map[string]retainedTree, error) {
	rows, err := database.QueryContext(ctx, `SELECT name,type,tbl_name FROM sqlite_schema WHERE rootpage>0 AND type IN ('table','index')
 UNION ALL SELECT 'sqlite_schema','table','sqlite_schema' ORDER BY 2 DESC`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
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
		return nil, err
	}
	measured := map[string]retainedTree{}
	for name, tree := range known {
		measured[name] = tree
	}
	for _, tree := range trees {
		if _, ok := measured[tree.name]; ok {
			continue
		}
		if routeOnly && (tree.kind != "table" || !retainedRouteTree(tree.name)) {
			continue
		}
		var bad uint64
		scanErr := database.QueryRowContext(ctx, `SELECT COALESCE(SUM(ncell),0),COALESCE(MAX(mx_payload),0),COALESCE(SUM(pgsize),0),
 COALESCE(SUM(CASE WHEN pagetype IS NULL OR pagetype NOT IN ('internal','leaf','overflow') THEN 1 ELSE 0 END),0)
 FROM dbstat WHERE name=?`, tree.name).Scan(&tree.cells, &tree.record, &tree.bytes, &bad)
		if scanErr != nil {
			var failure *sqlite.Error
			if tree.kind != "index" || !errors.As(scanErr, &failure) || failure.Code()&0xff != sqlite3.SQLITE_CORRUPT {
				return nil, scanErr
			}
			bad = 1
		}
		if bad != 0 {
			if tree.kind == "table" {
				return nil, fmt.Errorf("%w: retained primary tree %s is corrupt", ErrIntegrity, tree.name)
			}
			tree.record = max(tree.record, measured[tree.table].record)
		}
		measured[tree.name] = tree
	}
	return measured, nil
}
