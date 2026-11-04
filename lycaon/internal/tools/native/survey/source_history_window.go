package survey

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

const sourceHistoryWindowUnknownNote = "this session has no recorded turn boundary, so its window is unknown; source_history(path) has one file's record"

// sourceHistorySince is the exclusive ledger ordinal a session window starts after.
type sourceHistorySince struct {
	Ordinal int64 `json:"ordinal"`
}

// sessionWindow resolves the ordinal a session's changes start after. A worker
// branch is private to its job, so all of it happened during the leg; elsewhere
// the window starts at the session's first turn boundary. ok=false means the
// window is unknown, never that nothing changed.
func sessionWindow(ctx context.Context, tctx tools.ToolContext, ledger tools.SourceHistory) (ordinal int64, ok bool, err error) {
	if tctx.Source.SourceWorkspaceKind == api.SourceWorkspaceKindWorker {
		return 0, true, nil
	}
	ordinal, ok, err = ledger.Files.SessionActivityFloor(ctx, tctx.Identity.ProjectID, tctx.Identity.SessionID)
	if err != nil {
		return 0, false, fmt.Errorf("source history session window: %w", err)
	}
	return ordinal, ok, nil
}
