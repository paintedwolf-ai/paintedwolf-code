package migrations

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"

	"errors"
	"math"

	"github.com/google/uuid"
)

//go:embed outside_namespace.go
var outsideNamespaceImplementation []byte

//go:embed outside_namespace.sql
var outsideNamespaceSQL []byte

//go:embed table_rebuild.go
var tableRebuildImplementation []byte

//go:embed outside_rebuild.sql
var outsideRebuildSQL []byte

// OutsideNamespace gives every project one source namespace for files beyond
// its attached folders, and per-chat references to the files in it.
func OutsideNamespace() Part {
	return Part{
		Source:        append(append(append(append([]byte{}, outsideNamespaceImplementation...), outsideNamespaceSQL...), tableRebuildImplementation...), outsideRebuildSQL...),
		RebuildTables: true,
		Apply:         migrateOutsideNamespace,
		Validate:      validateOutsideNamespace,
		Writes:        outsideNamespaceWrites,
	}
}

func migrateOutsideNamespace(ctx context.Context, tx *sql.Tx) error {
	triggers, err := suspendTriggers(ctx, tx, "project_roots")
	if err != nil {
		return err
	}
	views, err := suspendViews(ctx, tx)
	if err != nil {
		return err
	}
	if err := rebuildTable(ctx, tx, "project_roots", outsideKindEdit); err != nil {
		return fmt.Errorf("rebuild project_roots for the outside namespace: %w", err)
	}
	if err := restoreSchemaObjects(ctx, tx, append(views, triggers...)); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, string(outsideNamespaceSQL)); err != nil {
		return err
	}
	return insertOutsideNamespaces(ctx, tx)
}

// outsideNamespaceIDs derives each migrated project's outside root id from the
// project id, so every upgrade strategy writes the same rows.
var outsideNamespaceIDs = uuid.MustParse("0d4e1f6b-3c2a-4f58-9b7e-6a1c2d3e4f50")

func insertOutsideNamespaces(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT id, created_at FROM projects ORDER BY id`)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	type project struct{ id, created string }
	for rows.Next() {
		var p project
		if err := rows.Scan(&p.id, &p.created); err != nil {
			return err
		}
		id := uuid.NewSHA1(outsideNamespaceIDs, []byte(p.id)).String()
		if _, err := tx.ExecContext(ctx, `INSERT INTO project_roots (id, project_id, path, label, is_primary, added_at, kind) VALUES (?, ?, '', 'outside', 0, ?, 'outside')`, id, p.id, p.created); err != nil {
			return err
		}
	}
	return errors.Join(rows.Err(), rows.Close())
}

// Charge the rebuilt root table, its indexes and one new root per project.
func outsideNamespaceWrites(ctx context.Context, store DBTX, _ uint64) (uint64, error) {
	var rootPages, projectPages uint64
	err := store.QueryRowContext(ctx, `SELECT
		COALESCE(SUM(CASE WHEN s.tbl_name = 'project_roots' THEN d.pgsize ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN s.name = 'projects' THEN d.pgsize ELSE 0 END), 0)
		FROM dbstat d JOIN sqlite_schema s ON s.name = d.name
		WHERE s.type = 'table' AND s.name IN ('project_roots', 'projects')`).Scan(&rootPages, &projectPages)
	if err != nil {
		return 0, err
	}
	pages, err := storageSum(rootPages, projectPages)
	if err != nil || pages > (math.MaxUint64-(1<<20))/16 {
		return 0, fmt.Errorf("outside namespace write estimate overflows")
	}
	return 16*pages + (1 << 20), nil
}

func outsideKindEdit(tail string) (string, error) {
	return replaceOnce(tail,
		"kind TEXT NOT NULL DEFAULT 'attached' CHECK (kind IN ('attached', 'draft')),",
		"kind TEXT NOT NULL DEFAULT 'attached' CHECK (kind IN ('attached', 'draft', 'outside')),\n"+
			"    CHECK ((kind = 'outside') = (path = '')),\n"+
			"    CHECK (kind != 'outside' OR is_primary = 0),")
}

func validateOutsideNamespace(ctx context.Context, tx *sql.Tx) error {
	var missing int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM projects p WHERE (
		SELECT COUNT(*) FROM project_roots r WHERE r.project_id = p.id AND r.kind = 'outside') != 1`).Scan(&missing); err != nil {
		return err
	}
	if missing != 0 {
		return fmt.Errorf("%d projects lack exactly one outside namespace", missing)
	}
	return nil
}
