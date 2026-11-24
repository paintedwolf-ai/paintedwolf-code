package migrations

import (
	"context"
	"database/sql"
	_ "embed"
)

//go:embed worktree_chat_usage.sql
var worktreeChatUsageSQL []byte

// WorktreeChatUsage adds additive per-chat and per-worker checkout association tables.
func WorktreeChatUsage() Part {
	return Part{
		Source: worktreeChatUsageSQL,
		Apply: func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, string(worktreeChatUsageSQL))
			return err
		},
		Writes: func(context.Context, DBTX, uint64) (uint64, error) {
			return 1 << 20, nil
		},
	}
}
