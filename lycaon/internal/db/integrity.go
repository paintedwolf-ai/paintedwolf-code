package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// This reserved host-written value shipped as the unfinished-audit marker.
// Other nonempty values record confirmed damage, never unfinished work.
const (
	integrityAuditFailedMetaKey = "integrity_audit_failed"
	integrityAuditPendingValue  = "integrity audit pending"
)

// prepareIntegrity refuses recorded damage without scanning historical rows.
// SQLite recovers its WAL; unclean shutdowns and unfinished audits are verified
// asynchronously after serving starts.
func prepareIntegrity(ctx context.Context, sqlDB *sql.DB) error {
	value, err := integrityAuditState(ctx, sqlDB)
	if err != nil {
		return fmt.Errorf("read integrity audit state: %w", err)
	}
	if value == "" || value == integrityAuditPendingValue {
		return nil
	}
	ver, _ := ReadUserVersion(ctx, sqlDB)
	return storeIncompatible(RecoveryReasonIntegrityFailed, ver, "A previous integrity audit confirmed damage: "+value)
}

// IntegrityAuditDue reports an unclean shutdown or an unfinished background audit.
func (s *Store) IntegrityAuditDue() bool {
	return s != nil && s.integrityAuditDue
}

// RunIntegrityAudit audits the store once after boot, then parks until ctx
// ends so the runner supervisor does not restart it.
func RunIntegrityAudit(ctx context.Context, store *Store) error {
	if err := AuditIntegrity(ctx, store); err != nil {
		return err
	}
	<-ctx.Done()
	return ctx.Err()
}

// AuditIntegrity quarantines confirmed damage and clears completed audit state.
func AuditIntegrity(ctx context.Context, store *Store) error {
	if store == nil || store.reader == nil {
		return errors.New("audit integrity: nil database")
	}
	store.audit.run.Lock()
	defer store.audit.run.Unlock()
	started := time.Now()
	attempt := 0
	auditErr := auditAttempts(ctx,
		func(ctx context.Context) error {
			attempt++
			release, err := store.audit.backgroundTurn(ctx)
			if err != nil {
				return err
			}
			defer release()
			limit, err := auditFrameBudget(ctx, store)
			if err != nil {
				return err
			}
			checkCtx, finish := store.audit.begin(ctx, limit)
			defer finish()
			return auditSnapshot(checkCtx, store.reader, nil)
		}, func(ctx context.Context) error { return waitForAuditCapacity(ctx, store, auditRetryDelay(attempt)) })
	if err := finishIntegrityAudit(ctx, store, auditErr); err != nil {
		return err
	}
	slog.InfoContext(ctx, "store integrity audit passed", "component", "db", "elapsed", time.Since(started))
	return nil
}

// Confirmed damage survives a concurrent shutdown; cancellation only leaves
// unfinished verification pending, never replaces an established diagnosis.
func finishIntegrityAudit(ctx context.Context, store *Store, auditErr error) error {
	var damaged *StoreIncompatibleError
	if errors.As(auditErr, &damaged) {
		store.quarantine(damaged)
		slog.ErrorContext(ctx, "store integrity failed; writes stopped", "component", "db", "error", auditErr)
		return auditErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if auditErr != nil {
		return auditErr
	}
	clearErr := clearIntegrityAuditFailed(ctx, store.writer)
	if failure := store.Failure(); failure != nil {
		return failure
	}
	if clearErr == nil {
		store.auditCompleted()
	}
	return clearErr
}

func integrityAuditState(ctx context.Context, sqlDB DBTX) (string, error) {
	var value string
	err := sqlDB.QueryRowContext(ctx, `SELECT value FROM store_meta WHERE key = ?`, integrityAuditFailedMetaKey).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return value, err
}

func clearIntegrityAuditFailed(ctx context.Context, sqlDB DBTX) error {
	_, err := sqlDB.ExecContext(ctx, `DELETE FROM store_meta WHERE key = ? AND value = ?`, integrityAuditFailedMetaKey, integrityAuditPendingValue)
	return err
}

// validateStoreStructure admits structurally readable stores without auditing
// correspondence between every secondary index and every historical table row.
func validateStoreStructure(ctx context.Context, sqlDB DBTX) error {
	if err := scanCheck(ctx, sqlDB, "PRAGMA quick_check"); err != nil {
		return err
	}
	return foreignKeyCheck(ctx, sqlDB, "PRAGMA foreign_key_check")
}

// auditSnapshot keeps page accounting and foreign keys on the same read view.
// betweenChecks is an observation seam; it never changes snapshot custody.
func auditSnapshot(ctx context.Context, database *sql.DB, betweenChecks func()) error {
	tx, err := database.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err == nil {
		defer func() { _ = tx.Rollback() }()
		err = scanCheck(ctx, tx, "PRAGMA integrity_check")
		if err == nil {
			if betweenChecks != nil {
				betweenChecks()
			}
			err = foreignKeyCheck(ctx, tx, "PRAGMA foreign_key_check")
		}
	}
	var damaged *StoreIncompatibleError
	if errors.As(err, &damaged) {
		return err
	}
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	return err
}

// scanCheck reads SQLite's integrity check: one "ok" row or one row per problem.
func scanCheck(ctx context.Context, sqlDB DBTX, statement string) error {
	pragma := strings.TrimPrefix(statement, "PRAGMA ")
	rows, err := sqlDB.QueryContext(ctx, statement) // #nosec G202 -- a fixed pragma over a listed table name
	if err != nil {
		return integrityCheckError(err, pragma)
	}
	defer func() { _ = rows.Close() }()
	var problems []string
	problemCount := 0
	for rows.Next() {
		var result string
		if err := rows.Scan(&result); err != nil {
			if problemCount > 0 {
				break
			}
			return integrityCheckError(err, "read "+pragma)
		}
		if result != "ok" {
			problemCount++
			if len(problems) < 3 {
				problems = append(problems, result)
			}
		}
	}
	if problemCount > 0 {
		ver, _ := ReadUserVersion(ctx, sqlDB)
		return storeIncompatible(RecoveryReasonIntegrityFailed, ver,
			fmt.Sprintf("%s failed: %s", pragma, summarizeIntegrityProblems(problems, problemCount)))
	}
	if err := rows.Err(); err != nil {
		return integrityCheckError(err, "scan "+pragma)
	}
	return nil
}

func foreignKeyCheck(ctx context.Context, sqlDB DBTX, statement string) error {
	rows, err := sqlDB.QueryContext(ctx, statement) // #nosec G202 -- a fixed pragma over a listed table name
	if err != nil {
		return integrityCheckError(err, "foreign_key_check")
	}
	defer func() { _ = rows.Close() }()
	var problems []string
	problemCount := 0
	for rows.Next() {
		var table, parent string
		var rowID sql.NullInt64
		var foreignKeyID int
		if err := rows.Scan(&table, &rowID, &parent, &foreignKeyID); err != nil {
			if problemCount > 0 {
				break
			}
			return integrityCheckError(err, "read foreign_key_check")
		}
		problemCount++
		if len(problems) < 3 {
			problems = append(problems, fmt.Sprintf("%s references %s", table, parent))
		}
	}
	if problemCount == 0 {
		if err := rows.Err(); err != nil {
			return integrityCheckError(err, "scan foreign_key_check")
		}
		return nil
	}
	ver, _ := ReadUserVersion(ctx, sqlDB)
	return storeIncompatible(RecoveryReasonIntegrityFailed, ver,
		fmt.Sprintf("foreign_key_check failed: %s", summarizeIntegrityProblems(problems, problemCount)))
}

func summarizeIntegrityProblems(problems []string, total int) string {
	summary := strings.Join(problems, "; ")
	if remaining := total - len(problems); remaining > 0 {
		return fmt.Sprintf("%s; and %d more", summary, remaining)
	}
	return summary
}
