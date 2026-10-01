package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// DBTX is the read boundary used for both a store and a migration transaction.
type DBTX interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	PrepareContext(context.Context, string) (*sql.Stmt, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type Phase string

const (
	Inspecting   Phase = "inspecting"
	Snapshotting Phase = "snapshotting"
	Migrating    Phase = "migrating"
	Validating   Phase = "validating"
	Ready        Phase = "ready"
	Failed       Phase = "failed"
)

// Inspect reads the revision and exact physical schema from the provided connection.
type Inspect func(context.Context, DBTX) (Baseline, error)

// CheckLedger verifies recorded migration identities and checksums.
func (r *Registry) CheckLedger(ctx context.Context, database DBTX, revision int) error {
	rows, err := database.QueryContext(ctx, `SELECT id, checksum, from_revision, to_revision FROM schema_migrations ORDER BY to_revision`)
	if err != nil {
		return fmt.Errorf("read migration ledger: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id, checksum string
		var from, to int
		if err := rows.Scan(&id, &checksum, &from, &to); err != nil {
			return err
		}
		step, ok := r.byID[id]
		if !ok || step.Checksum != checksum || step.From.Revision != from || step.To.Revision != to || to > revision {
			return fmt.Errorf("migration ledger entry %q differs from registered history", id)
		}
	}
	return rows.Err()
}

// Execute applies the whole route atomically inside one immediate transaction;
// a live installation is snapshotted before it runs. Staged and live callers share it.
func (r *Registry) Execute(ctx context.Context, database *sql.DB, planned Plan, inspect Inspect, progress func(Phase)) (err error) {
	if !planned.Required() {
		return nil
	}
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin schema upgrade: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
		if err != nil && progress != nil {
			progress(Failed)
		}
	}()
	actual, err := inspect(ctx, tx)
	if err != nil {
		return err
	}
	if actual != planned.Source {
		return fmt.Errorf("schema changed after upgrade planning")
	}
	plan, err := r.Plan(actual)
	if err != nil {
		return err
	}
	if err := r.CheckLedger(ctx, tx, actual.Revision); err != nil {
		return err
	}
	for _, step := range plan.steps {
		if err := applyStep(ctx, tx, step, inspect, progress); err != nil {
			return fmt.Errorf("migration %s: %w", step.ID, err)
		}
	}
	if err := checkForeignKeys(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit schema upgrade: %w", err)
	}
	return nil
}

func applyStep(ctx context.Context, tx *sql.Tx, step Step, inspect Inspect, progress func(Phase)) error {
	if progress != nil {
		progress(Migrating)
	}
	if err := step.Apply(ctx, tx); err != nil {
		return err
	}
	if progress != nil {
		progress(Validating)
	}
	if step.Validate != nil {
		if err := step.Validate(ctx, tx); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, step.To.Revision)); err != nil {
		return err
	}
	actual, err := inspect(ctx, tx)
	if err != nil {
		return err
	}
	if actual != step.To {
		return fmt.Errorf("destination schema differs from registered revision %d", step.To.Revision)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO schema_migrations(id, checksum, from_revision, to_revision, applied_at) VALUES(?,?,?,?,?)`,
		step.ID, step.Checksum, step.From.Revision, step.To.Revision, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func checkForeignKeys(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return fmt.Errorf("check upgraded foreign keys: %w", err)
	}
	defer func() { _ = rows.Close() }()
	if rows.Next() {
		return fmt.Errorf("upgraded store contains a foreign key violation")
	}
	return rows.Err()
}
