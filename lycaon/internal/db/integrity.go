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

// integrityAuditFailedMetaKey records that a background audit found damage.
// The next boot then runs the whole-store check before serving, which is the
// path that reaches the recovery surface.
const integrityAuditFailedMetaKey = "integrity_audit_failed"

// prepareIntegrity is the boot gate for an existing store: nothing after a
// clean shutdown, quick_check after an unclean one with the whole-store audit
// deferred until serving starts, and the whole check when a failed audit left
// its stamp.
func prepareIntegrity(ctx context.Context, sqlDB *sql.DB) error {
	stamped, err := integrityAuditFailed(ctx, sqlDB)
	if err != nil {
		return fmt.Errorf("read integrity audit state: %w", err)
	}
	if stamped {
		return validateStoreIntegrity(ctx, sqlDB)
	}
	clean, err := lastShutdownWasClean(ctx, sqlDB)
	if err != nil {
		return fmt.Errorf("read shutdown state: %w", err)
	}
	if clean {
		return nil
	}
	return quickCheck(ctx, sqlDB)
}

// IntegrityAuditDue reports whether this open followed an unclean shutdown,
// so the whole-store audit still has to run in the background.
func (s *Store) IntegrityAuditDue() bool {
	return s != nil && s.integrityAuditDue
}

// RunIntegrityAudit audits the store once after boot, then parks until ctx
// ends so the runner supervisor does not restart it.
func RunIntegrityAudit(ctx context.Context, store *Store) error {
	if err := AuditIntegrity(ctx, store); err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	<-ctx.Done()
	return ctx.Err()
}

// AuditIntegrity runs integrity_check and foreign_key_check on the reader
// pool and returns the result. Damage is logged and stamped so the next boot
// refuses the store on the path that reaches recovery; a clean audit clears
// the stamp.
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
		slog.ErrorContext(ctx, "store integrity audit found damage; the next start runs recovery",
			"component", "db", "error", auditErr, "elapsed", time.Since(started))
		if err := stampIntegrityAuditFailed(ctx, store.writer, auditErr); err != nil {
			slog.ErrorContext(ctx, "store integrity audit could not record its result", "component", "db", "error", err)
		}
		return auditErr
	}
	slog.InfoContext(ctx, "store integrity audit passed", "component", "db", "elapsed", time.Since(started))
	if err := clearIntegrityAuditFailed(ctx, store.writer); err != nil {
		slog.WarnContext(ctx, "store integrity audit could not clear its stamp", "component", "db", "error", err)
	}
	return nil
}

func integrityAuditFailed(ctx context.Context, sqlDB DBTX) (bool, error) {
	var value string
	err := sqlDB.QueryRowContext(ctx, `SELECT value FROM store_meta WHERE key = ?`, integrityAuditFailedMetaKey).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil && value != "", err
}

func stampIntegrityAuditFailed(ctx context.Context, sqlDB DBTX, auditErr error) error {
	_, err := sqlDB.ExecContext(ctx, `
INSERT INTO store_meta(key, value) VALUES(?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value`, integrityAuditFailedMetaKey, auditErr.Error())
	return err
}

func clearIntegrityAuditFailed(ctx context.Context, sqlDB DBTX) error {
	_, err := sqlDB.ExecContext(ctx, `DELETE FROM store_meta WHERE key = ?`, integrityAuditFailedMetaKey)
	return err
}

// quickCheck verifies page and B-tree structure without cross-checking index
// content against tables; damage that leaves the structure intact is caught
// by the whole-store audit after boot.
func quickCheck(ctx context.Context, sqlDB *sql.DB) error {
	return scanCheck(ctx, sqlDB, "quick_check")
}

func validateStoreIntegrity(ctx context.Context, sqlDB *sql.DB) error {
	if err := scanCheck(ctx, sqlDB, "integrity_check"); err != nil {
		return err
	}
	return foreignKeyCheck(ctx, sqlDB)
}

// scanCheck runs one of SQLite's self-check pragmas, which report "ok" as a
// single row and otherwise one row per problem.
func scanCheck(ctx context.Context, sqlDB *sql.DB, pragma string) error {
	rows, err := sqlDB.QueryContext(ctx, "PRAGMA "+pragma) // #nosec G202 -- pragma is one of two package constants
	if err != nil {
		ver, _ := ReadUserVersion(ctx, sqlDB)
		return storeIncompatible(RecoveryReasonIntegrityFailed, ver, fmt.Sprintf("%s: %v", pragma, err))
	}
	defer func() { _ = rows.Close() }()
	var problems []string
	problemCount := 0
	for rows.Next() {
		var result string
		if err := rows.Scan(&result); err != nil {
			ver, _ := ReadUserVersion(ctx, sqlDB)
			return storeIncompatible(RecoveryReasonIntegrityFailed, ver, fmt.Sprintf("read %s: %v", pragma, err))
		}
		if result != "ok" {
			problemCount++
			if len(problems) < 3 {
				problems = append(problems, result)
			}
		}
	}
	if err := rows.Err(); err != nil {
		ver, _ := ReadUserVersion(ctx, sqlDB)
		return storeIncompatible(RecoveryReasonIntegrityFailed, ver, fmt.Sprintf("scan %s: %v", pragma, err))
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
		ver, _ := ReadUserVersion(ctx, sqlDB)
		return storeIncompatible(RecoveryReasonIntegrityFailed, ver, fmt.Sprintf("foreign_key_check: %v", err))
	}
	defer func() { _ = rows.Close() }()
	var problems []string
	problemCount := 0
	for rows.Next() {
		var table, parent string
		var rowID sql.NullInt64
		var foreignKeyID int
		if err := rows.Scan(&table, &rowID, &parent, &foreignKeyID); err != nil {
			ver, _ := ReadUserVersion(ctx, sqlDB)
			return storeIncompatible(RecoveryReasonIntegrityFailed, ver, fmt.Sprintf("read foreign_key_check: %v", err))
		}
		problemCount++
		if len(problems) < 3 {
			problems = append(problems, fmt.Sprintf("%s references %s", table, parent))
		}
	}
	if err := rows.Err(); err != nil {
		ver, _ := ReadUserVersion(ctx, sqlDB)
		return storeIncompatible(RecoveryReasonIntegrityFailed, ver, fmt.Sprintf("scan foreign_key_check: %v", err))
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
