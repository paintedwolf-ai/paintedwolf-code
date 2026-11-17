package migrations

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
)

//go:embed outside_namespace.go
var outsideNamespaceImplementation []byte

//go:embed outside_namespace.sql
var outsideNamespaceSQL []byte

//go:embed table_rebuild.go
var tableRebuildImplementation []byte

// OutsideNamespace gives every project one source namespace for files beyond
// its attached folders, and per-chat references to the files in it.
func OutsideNamespace() Part {
	return Part{
		Source:        append(append(append([]byte{}, outsideNamespaceImplementation...), outsideNamespaceSQL...), tableRebuildImplementation...),
		RebuildTables: true,
		Apply:         migrateOutsideNamespace,
		Validate:      validateOutsideNamespace,
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
	_, err = tx.ExecContext(ctx, string(outsideNamespaceSQL))
	return err
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
