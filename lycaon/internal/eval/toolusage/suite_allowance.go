package toolusage

import (
	"context"
	"database/sql"
	"github.com/lycaon/lycaon/internal/eval/episode"
)

func observeTaskAllowance(ctx context.Context, database *sql.DB, sessionID string) error {
	receipt, err := episode.ReadAllowance(ctx, database, sessionID)
	if err != nil {
		return err
	}
	if receipt.ExhaustedAttemptID != "" {
		return &ExecutionFailure{Kind: "model", Code: "task_allowance_exhausted"}
	}
	return nil
}
