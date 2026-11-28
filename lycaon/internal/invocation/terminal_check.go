package invocation

import (
	"context"
	"strings"
)

// AllToolCallsEnded reports whether every recorded invocation receipt for the
// given session and tool_call_id set has durably left the running state. It
// returns false when no matching receipt exists, so approvals without a
// durable invocation receipt are not treated as orphaned.
func (r *SQLRecorder) AllToolCallsEnded(ctx context.Context, sessionID string, toolCallIDs []string) (bool, error) {
	sessionID = strings.TrimSpace(sessionID)
	if r == nil || r.database == nil || sessionID == "" || len(toolCallIDs) == 0 {
		return false, nil
	}
	args := make([]any, 0, len(toolCallIDs)+1)
	args = append(args, sessionID)
	placeholders := make([]string, 0, len(toolCallIDs))
	for _, id := range toolCallIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		placeholders = append(placeholders, "?")
		args = append(args, id)
	}
	if len(placeholders) == 0 {
		return false, nil
	}
	query := `SELECT status FROM invocation_receipts WHERE session_id = ? AND tool_call_id IN (` + strings.Join(placeholders, ",") + `)`
	rows, err := r.database.QueryContext(ctx, query, args...)
	if err != nil {
		return false, err
	}
	defer func() { _ = rows.Close() }()

	matched := 0
	for rows.Next() {
		var status string
		if err := rows.Scan(&status); err != nil {
			return false, err
		}
		matched++
		if status == "running" {
			return false, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	return matched > 0, nil
}
