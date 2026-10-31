package db

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"

	"github.com/lycaon/lycaon/internal/db/migrations"
)

//go:embed rebuild_chunk_inventory.sql
var rebuildChunkInventorySQL string

// The private main database owns this inventory, so SQLite's admitted page
// limit covers deduplication as well as retained rows and indexes. DISTINCT
// and IN subqueries would put an independent B-tree on the temp volume.
func prepareRebuildChunkInventory(ctx context.Context, tx *sql.Tx, selection *migrations.RetainedCopy) (*migrations.RetainedCopy, error) {
	_, err := tx.ExecContext(ctx, rebuildChunkInventorySQL)
	if err != nil {
		return nil, err
	}
	out := *selection
	out.Sources = map[string]string{}
	for key, value := range selection.Sources {
		out.Sources[key] = value
	}
	out.ExpectedRows = map[string]string{}
	for key, value := range selection.ExpectedRows {
		out.ExpectedRows[key] = value
	}
	out.Sources["source_manifest_chunks"] = `FROM original.source_manifest_chunks e NOT INDEXED WHERE EXISTS (SELECT 1 FROM main._upgrade_retained_chunks c WHERE c.id=e.id)`
	out.Sources["source_manifest_entries"] = `FROM main._upgrade_retained_chunks c CROSS JOIN original.source_manifest_entries e ON e.chunk_id=c.id`
	out.ExpectedRows["source_manifest_chunks"] = `SELECT COUNT(*) FROM main._upgrade_retained_chunks`
	out.ExpectedRows["source_manifest_entries"] = `SELECT COALESCE(SUM(entry_count),0) FROM original.source_manifest_chunks m NOT INDEXED WHERE EXISTS (SELECT 1 FROM main._upgrade_retained_chunks c WHERE c.id=m.id)`
	return &out, nil
}

func validateRebuildChunkInventory(ctx context.Context, tx *sql.Tx) error {
	var missing bool
	// Every admitted ID originates in the fixed INSERT SELECT above. Check
	// coverage independently from the copying predicates without a quadratic
	// reverse lookup through the original references.
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM original.source_snapshot_chunks c NOT INDEXED WHERE NOT EXISTS (SELECT 1 FROM main._upgrade_retained_chunks i WHERE i.id=c.chunk_id))`).Scan(&missing)
	if err != nil {
		return err
	}
	if missing {
		return fmt.Errorf("%w: retained chunk inventory differs from source references", migrations.ErrIntegrity)
	}
	return nil
}
