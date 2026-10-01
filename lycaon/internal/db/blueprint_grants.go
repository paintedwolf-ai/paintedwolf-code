package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Blueprint approval revocation causes.
const (
	BlueprintGrantCauseContentChanged   = "content_changed"
	BlueprintGrantCauseSessionDeleted   = "session_deleted"
	BlueprintGrantCauseBlueprintDeleted = "blueprint_deleted"
)

// RevokeBlueprintApprovalsForSession revokes active grants for a deleted session.
func RevokeBlueprintApprovalsForSession(ctx context.Context, tx *sql.Tx, sessionID string, now time.Time) (int64, error) {
	res, err := tx.ExecContext(ctx, `
		UPDATE blueprint_approvals
		SET status = 'revoked', revoked_at = ?, revoked_cause = ?
		WHERE session_id = ? AND status = 'approved'`,
		FormatTime(now.UTC()), BlueprintGrantCauseSessionDeleted, sessionID)
	if err != nil {
		return 0, fmt.Errorf("revoke blueprint approvals for session: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// revokeOrphanBlueprintGrants revokes grants whose session is gone.
func revokeOrphanBlueprintGrants(ctx context.Context, sqlDB Handle, now time.Time) (int64, error) {
	res, err := sqlDB.ExecContext(ctx, `
		UPDATE blueprint_approvals
		SET status = 'revoked', revoked_at = ?, revoked_cause = ?
		WHERE status = 'approved' AND session_id NOT IN (SELECT id FROM sessions)`,
		FormatTime(now.UTC()), BlueprintGrantCauseSessionDeleted)
	if err != nil {
		return 0, fmt.Errorf("revoke orphan blueprint grants: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}
