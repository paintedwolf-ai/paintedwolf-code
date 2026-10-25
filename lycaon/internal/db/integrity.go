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
	started := time.Now()
	auditErr := validateStoreIntegrity(ctx, store.reader)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if auditErr != nil {
		var damaged *StoreIncompatibleError
		if !errors.As(auditErr, &damaged) {
			return auditErr
		}
		store.quarantine(damaged)
		slog.ErrorContext(ctx, "store integrity failed; writes stopped",
			"component", "db", "error", auditErr, "elapsed", time.Since(started))
		return auditErr
	}
	slog.InfoContext(ctx, "store integrity audit passed", "component", "db", "elapsed", time.Since(started))
	if err := clearIntegrityAuditFailed(ctx, store.writer); err != nil {
		slog.WarnContext(ctx, "store integrity audit could not clear its stamp", "component", "db", "error", err)
	}
	return nil
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
	_, err := sqlDB.ExecContext(ctx, `DELETE FROM store_meta WHERE key = ?`, integrityAuditFailedMetaKey)
	return err
}

func validateStoreIntegrity(ctx context.Context, sqlDB *sql.DB) error {
	if err := scanCheck(ctx, sqlDB); err != nil {
		return err
	}
	return foreignKeyCheck(ctx, sqlDB)
}

// scanCheck reads SQLite's integrity check: one "ok" row or one row per problem.
func scanCheck(ctx context.Context, sqlDB *sql.DB) error {
	const pragma = "integrity_check"
	rows, err := sqlDB.QueryContext(ctx, "PRAGMA integrity_check")
	if err != nil {
		return integrityCheckError(err, pragma)
	}
	defer func() { _ = rows.Close() }()
	var problems []string
	problemCount := 0
	for rows.Next() {
		var result string
		if err := rows.Scan(&result); err != nil {
			return integrityCheckError(err, "read "+pragma)
		}
		if result != "ok" {
			problemCount++
			if len(problems) < 3 {
				problems = append(problems, result)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return integrityCheckError(err, "scan "+pragma)
	}
	if problemCount > 0 {
		ver, _ := ReadUserVersion(ctx, sqlDB)
		return storeIncompatible(RecoveryReasonIntegrityFailed, ver,
			fmt.Sprintf("%s failed: %s", pragma, summarizeIntegrityProblems(problems, problemCount)))
	}
	return nil
}

func foreignKeyCheck(ctx context.Context, sqlDB *sql.DB) error {
	rows, err := sqlDB.QueryContext(ctx, `PRAGMA foreign_key_check`)
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
			return integrityCheckError(err, "read foreign_key_check")
		}
		problemCount++
		if len(problems) < 3 {
			problems = append(problems, fmt.Sprintf("%s references %s", table, parent))
		}
	}
	if err := rows.Err(); err != nil {
		return integrityCheckError(err, "scan foreign_key_check")
	}
	if problemCount == 0 {
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
