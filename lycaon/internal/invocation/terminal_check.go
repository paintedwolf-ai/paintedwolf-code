package invocation

import (
	"context"
	"strings"
)

// isTerminalReceiptStatus reports whether a durable invocation receipt status
// belongs to the receipt owner's terminal status set.
func isTerminalReceiptStatus(status string) bool {
	switch status {
	case "completed", "failed", "canceled", "interrupted":
		return true
	default:
		return false
	}
}

// AllToolCallsEnded reports whether every distinct requested tool_call_id in
// the session has positive durable terminal evidence in invocation_receipts
// (and none is still running or missing a receipt).
func (r *SQLRecorder) AllToolCallsEnded(ctx context.Context, sessionID string, toolCallIDs []string) (bool, error) {
	sessionID = strings.TrimSpace(sessionID)
	if r == nil || r.database == nil || sessionID == "" || len(toolCallIDs) == 0 {
		return false, nil
	}
	requested := make(map[string]struct{}, len(toolCallIDs))
	args := make([]any, 0, len(toolCallIDs)+1)
	args = append(args, sessionID)
	placeholders := make([]string, 0, len(toolCallIDs))
	for _, id := range toolCallIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, dup := requested[id]; dup {
			continue
		}
		requested[id] = struct{}{}
		placeholders = append(placeholders, "?")
		args = append(args, id)
	}
	if len(requested) == 0 {
		return false, nil
	}
	query := `SELECT tool_call_id, status FROM invocation_receipts WHERE session_id = ? AND tool_call_id IN (` + strings.Join(placeholders, ",") + `)`
	rows, err := r.database.QueryContext(ctx, query, args...)
	if err != nil {
		return false, err
	}
	defer func() { _ = rows.Close() }()

	terminal := make(map[string]bool, len(requested))
	for rows.Next() {
		var toolCallID, status string
		if err := rows.Scan(&toolCallID, &status); err != nil {
			return false, err
		}
		if !isTerminalReceiptStatus(status) {
			return false, nil
		}
		terminal[toolCallID] = true
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	return len(terminal) == len(requested), nil
}
