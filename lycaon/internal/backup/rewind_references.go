package backup

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/session/checkpoint"
	"github.com/lycaon/lycaon/internal/session/store"
)

func validateRewindJournals(ctx context.Context, snapshot db.DBTX, files map[string]archiveSource) error {
	rows, err := snapshot.QueryContext(ctx, `SELECT id,session_id,anchor_message_id,project_dir FROM rewind_operations WHERE status IN ('prepared','applying','files_applied','diverged')`)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var op store.RewindOperation
		if err := rows.Scan(&op.ID, &op.SessionID, &op.AnchorMessageID, &op.ProjectDir); err != nil {
			return err
		}
		rel, err := checkpoint.JournalRelPath(op.ProjectDir, op.ID)
		if err != nil {
			return &InvalidError{Detail: err.Error()}
		}
		file, ok := files[filepath.ToSlash(rel)]
		if !ok || file.kind != fileKindRegular {
			return &InvalidError{Detail: "rewind journal is missing: " + op.ID}
		}
		journal, err := checkpoint.LoadJournal(file.path)
		if err == nil {
			err = journal.ValidateOperation(op)
		}
		if err != nil {
			return &InvalidError{Detail: fmt.Sprintf("rewind journal %s: %v", op.ID, err)}
		}
	}
	return rows.Err()
}
