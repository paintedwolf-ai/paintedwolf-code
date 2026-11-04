package survey

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/tools"
)

const sourceHistoryWindowUnknownNote = "this session has no recorded turn boundary; the window is unknown"

// sourceHistorySince is the exclusive ledger ordinal a session window starts after.
type sourceHistorySince struct {
	Ordinal int64 `json:"ordinal"`
}

// sessionWindow resolves the ordinal after the session's first turn boundary.
// ok=false means the window is unknown, never that nothing changed.
func sessionWindow(ctx context.Context, tctx tools.ToolContext, ledger tools.SourceHistory) (ordinal int64, ok bool, err error) {
	ordinal, ok, err = ledger.Files.SessionActivityFloor(ctx, tctx.Identity.ProjectID, tctx.Identity.SessionID)
	if err != nil {
		return 0, false, fmt.Errorf("source history session window: %w", err)
	}
	return ordinal, ok, nil
}
