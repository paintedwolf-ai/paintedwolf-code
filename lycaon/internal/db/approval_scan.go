package db

import "context"

// ScanChatGrants keeps one grant's metadata live while its visitor restores authority.
func (q *Queries) ScanChatGrants(ctx context.Context, visit func(ListChatGrantsRow) error) error {
	rows, err := q.db.QueryContext(ctx, listChatGrants)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var row ListChatGrantsRow
		if err := rows.Scan(&row.ChatSessionID, &row.ID, &row.CheckpointID, &row.DeltaJson, &row.ExpiresAt, &row.AuthorityFormat); err != nil {
			return err
		}
		if err := visit(row); err != nil {
			return err
		}
	}
	return rows.Err()
}

// ScanPreparedApprovalOperations preserves commit order without retaining a batch.
func (q *Queries) ScanPreparedApprovalOperations(ctx context.Context, visit func(ListPreparedApprovalOperationsRow) error) error {
	rows, err := q.db.QueryContext(ctx, listPreparedApprovalOperations)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var row ListPreparedApprovalOperationsRow
		if err := rows.Scan(&row.CheckpointID, &row.SessionID, &row.OptionJson, &row.AuthorityFormat); err != nil {
			return err
		}
		if err := visit(row); err != nil {
			return err
		}
	}
	return rows.Err()
}
