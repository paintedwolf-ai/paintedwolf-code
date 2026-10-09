package board

import (
	"strings"

	"github.com/lycaon/lycaon/internal/tools"
)

// CoordinatorBoardSessionID resolves the coordinator session key for board slices.
func CoordinatorBoardSessionID(tctx tools.ToolContext) string {
	if sid := strings.TrimSpace(tctx.Identity.HandoffSessionID); sid != "" {
		return sid
	}
	return strings.TrimSpace(tctx.Identity.SessionID)
}
