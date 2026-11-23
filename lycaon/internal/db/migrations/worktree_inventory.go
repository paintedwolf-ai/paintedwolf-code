package migrations

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"math"
)

//go:embed worktree_inventory.go
var worktreeInventoryImplementation []byte

//go:embed worktree_inventory.sql
var worktreeInventorySQL []byte

// WorktreeInventory adds registration incarnation and lifecycle evidence to source_worktrees.
func WorktreeInventory() Part {
	return Part{
		Source:        append(append([]byte{}, worktreeInventoryImplementation...), worktreeInventorySQL...),
		RebuildTables: true,
		Apply:         migrateWorktreeInventory,
		Writes:        worktreeInventoryWrites,
	}
}

func migrateWorktreeInventory(ctx context.Context, tx *sql.Tx) error {
	triggers, err := suspendTriggers(ctx, tx, "source_worktrees")
	if err != nil {
		return err
	}
	views, err := suspendViews(ctx, tx)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, string(worktreeInventorySQL)); err != nil {
		return fmt.Errorf("rebuild source_worktrees for checkout inventory: %w", err)
	}
	return restoreSchemaObjects(ctx, tx, append(views, triggers...))
}

func worktreeInventoryWrites(ctx context.Context, store DBTX, _ uint64) (uint64, error) {
	var pages uint64
	if err := store.QueryRowContext(ctx, `SELECT COALESCE(SUM(d.pgsize), 0)
		FROM dbstat d JOIN sqlite_schema s ON s.name = d.name
		WHERE s.tbl_name = 'source_worktrees'`).Scan(&pages); err != nil {
		return 0, err
	}
	if pages > (math.MaxUint64-(1<<20))/16 {
		return 0, fmt.Errorf("worktree inventory write estimate overflows")
	}
	return 16*pages + (1 << 20), nil
}
