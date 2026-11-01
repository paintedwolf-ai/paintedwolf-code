package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/lycaon/lycaon/internal/db/migrations"
)

type rebuildObject struct {
	kind, name, definition, tableKind string
	withoutRowID                      bool
}

// copyRetainedStore creates a caller-owned staging file. The installation lease
// must keep the source immutable until publication; the source is attached read-only.
// The copy keeps the released shape so the registered migration remains the sole
// schema transformer, but never visits chunks that transformation would discard.
func copyRetainedStore(ctx context.Context, source, destination string, plan migrations.Plan, progress func(migrations.Phase)) (retainedCopyResult, error) {
	if _, err := os.Lstat(destination); err == nil { // #nosec G703
		return retainedCopyResult{}, os.ErrExist
	} else if !os.IsNotExist(err) {
		return retainedCopyResult{}, err
	}
	if boundedRetainedRoute(plan) {
		result, used, err := copyWholeRetainedStore(ctx, source, destination, plan)
		if used || err != nil {
			return result, err
		}
	}
	if progress != nil {
		progress(migrations.Migrating)
	}
	return retainedCopyResult{}, copyRetainedRows(ctx, source, destination, plan)
}

func copyRetainedRows(ctx context.Context, source, destination string, plan migrations.Plan) (err error) {
	selection := plan.CopyPlan()
	if selection == nil {
		return fmt.Errorf("upgrade route does not define a retained copy")
	}
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) // #nosec G304,G703
	if err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	query := "_pragma=synchronous(full)&_pragma=auto_vacuum(incremental)&_pragma=temp_store(file)&_pragma=threads(0)&_pragma=cache_size(-2000)"
	if boundedRetainedRoute(plan) {
		query += "&_pragma=journal_mode(off)"
	}
	copyDB, err := sql.Open("sqlite", fileDSN(destination, query))
	if err != nil {
		return err
	}
	copyDB.SetMaxOpenConns(1)
	defer func() { err = errors.Join(err, copyDB.Close()) }()
	reserve := uint64(0)
	if boundedRetainedRoute(plan) {
		sourceReader, openErr := openReader(ctx, source)
		if openErr != nil {
			return openErr
		}
		reserve, err = plan.RetainedCopyScratch(ctx, sourceReader)
		err = errors.Join(err, sourceReader.Close())
		if err != nil {
			return err
		}
		if err := checkUpgradeTempStorage(ctx, copyDB, reserve); err != nil {
			return err
		}
	}
	restoreLimit, err := constrainRebuildCopy(ctx, copyDB, destination, reserve)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, restoreLimit()) }()
	return copyRetainedDatabase(ctx, copyDB, source, plan)
}

func copyRetainedDatabase(ctx context.Context, copyDB *sql.DB, source string, plan migrations.Plan) error {
	selection := plan.CopyPlan()
	if _, err := copyDB.ExecContext(ctx, `ATTACH DATABASE ? AS original`, fileDSN(source, "mode=ro")); err != nil {
		return err
	}
	objects, err := readRebuildObjects(ctx, copyDB)
	if err != nil {
		return err
	}
	tx, err := copyDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, object := range objects {
		if object.kind == "table" && object.tableKind != "shadow" && !strings.HasPrefix(object.name, "sqlite_") {
			if _, err := tx.ExecContext(ctx, object.definition); err != nil {
				return fmt.Errorf("create retained table %s: %w", object.name, err)
			}
		}
	}
	if boundedRetainedRoute(plan) {
		selection, err = prepareRebuildChunkInventory(ctx, tx, selection)
		if err != nil {
			return err
		}
	}
	for _, object := range objects {
		if object.kind != "table" || object.tableKind == "virtual" || object.name == "sqlite_sequence" {
			continue
		}
		if err := copyRebuildTable(ctx, tx, object, selection.Sources[object.name]); err != nil {
			return err
		}
	}
	// AUTOINCREMENT inserts above may advance counters; restore the source's
	// exact counters last, including sequence values above the highest live row.
	for _, object := range objects {
		if object.name == "sqlite_sequence" {
			if err := copyRebuildTable(ctx, tx, object, ""); err != nil {
				return err
			}
		}
	}
	if boundedRetainedRoute(plan) {
		if err := validateRebuildChunkInventory(ctx, tx); err != nil {
			return err
		}
	}
	if err := validateRebuildCounts(ctx, tx, objects, selection); err != nil {
		return err
	}
	if boundedRetainedRoute(plan) {
		if _, err := tx.ExecContext(ctx, "DROP TABLE _upgrade_retained_chunks"); err != nil {
			return err
		}
	}
	for _, statement := range selection.After {
		if _, err := tx.ExecContext(ctx, statement); err != nil { // #nosec G701
			return err
		}
	}
	for _, object := range objects {
		if object.kind != "table" {
			if _, err := tx.ExecContext(ctx, object.definition); err != nil {
				return fmt.Errorf("restore retained %s %s: %w", object.kind, object.name, err)
			}
		}
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version=%d`, plan.Source.Revision)); err != nil { // #nosec G701
		return err
	}
	actual, err := inspectSchema(ctx, tx)
	if err != nil {
		return err
	}
	if actual != plan.Source {
		return fmt.Errorf("retained copy changed the released schema")
	}
	if err := prepareAuditState(ctx, tx, true); err != nil {
		return err
	}
	return tx.Commit()
}

func readRebuildObjects(ctx context.Context, database *sql.DB) ([]rebuildObject, error) {
	rows, err := database.QueryContext(ctx, `SELECT s.type, s.name, s.sql, COALESCE(t.type,''), COALESCE(t.wr,0)
FROM original.sqlite_schema s LEFT JOIN pragma_table_list t ON t.schema='original' AND t.name=s.name
WHERE s.sql IS NOT NULL ORDER BY s.rowid`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var objects []rebuildObject
	for rows.Next() {
		var object rebuildObject
		if err := rows.Scan(&object.kind, &object.name, &object.definition, &object.tableKind, &object.withoutRowID); err != nil {
			return nil, err
		}
		objects = append(objects, object)
	}
	return objects, rows.Err()
}

func copyRebuildTable(ctx context.Context, tx *sql.Tx, object rebuildObject, from string) error {
	columns, err := rebuildColumns(ctx, tx, object)
	if err != nil {
		return err
	}
	var selected []string
	for _, column := range columns {
		selected = append(selected, "e."+column)
	}
	if from == "" {
		from = "FROM original." + rebuildIdentifier(object.name) + " e NOT INDEXED"
	}
	// FTS creation seeds shadow rows. Replace them with the exact source rows;
	// copying the virtual table itself would rebuild a potentially different index.
	if _, err := tx.ExecContext(ctx, "DELETE FROM "+rebuildIdentifier(object.name)); err != nil { // #nosec G202
		return err
	}
	statement := "INSERT INTO " + rebuildIdentifier(object.name) + " (" + strings.Join(columns, ",") + ") SELECT " + strings.Join(selected, ",") + " " + from // #nosec G202
	if _, err := tx.ExecContext(ctx, statement); err != nil {                                                                                                 // #nosec G701
		return fmt.Errorf("copy retained table %s: %w", object.name, err)
	}
	return nil
}

func rebuildColumns(ctx context.Context, tx *sql.Tx, object rebuildObject) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT name FROM pragma_table_xinfo(?, 'original') WHERE hidden=0 ORDER BY cid`, object.name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var columns []string
	rowID := !object.withoutRowID
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		if strings.EqualFold(name, "rowid") || strings.EqualFold(name, "_rowid_") || strings.EqualFold(name, "oid") {
			rowID = false
		}
		columns = append(columns, rebuildIdentifier(name))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if rowID {
		columns = append([]string{"rowid"}, columns...)
	}
	return columns, nil
}

func rebuildIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
