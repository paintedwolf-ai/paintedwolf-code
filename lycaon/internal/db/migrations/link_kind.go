package migrations

import (
	"context"
	"database/sql"
	"fmt"
)

// migrateLinkKind rebuilds the four source tables with link entries admitted.
func migrateLinkKind(ctx context.Context, tx *sql.Tx) error {
	triggers, err := suspendTriggers(ctx, tx, "source_files", "source_versions", "source_head_entries", "source_effects")
	if err != nil {
		return err
	}
	views, err := suspendViews(ctx, tx)
	if err != nil {
		return err
	}
	for _, table := range []string{"source_files", "source_versions", "source_head_entries", "source_effects"} {
		if err := rebuildTable(ctx, tx, table, linkKindEdit(table)); err != nil {
			return fmt.Errorf("rebuild %s for link entries: %w", table, err)
		}
	}
	return restoreSchemaObjects(ctx, tx, append(views, triggers...))
}

func linkKindEdit(table string) func(string) (string, error) {
	return func(tail string) (string, error) {
		switch table {
		case "source_files", "source_effects":
			return replaceOnce(tail, "entry_kind IN ('file', 'directory')", "entry_kind IN ('file', 'directory', 'link')")
		}
		tail, err := replaceOnce(tail, "state IN ('content', 'directory', 'absent', 'unresolved')", "state IN ('content', 'directory', 'link', 'absent', 'unresolved')")
		if err != nil || table != "source_versions" {
			return tail, err
		}
		return replaceOnce(tail, "state = 'content' AND", "state IN ('content', 'link') AND")
	}
}
