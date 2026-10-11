package tools

import (
	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

// DiscoveryPage scopes cursors to the caller and its current permitted catalog.
func DiscoveryPage(tctx ToolContext, tool, need, cursor, status string, failure turnload.RankingFailure, entries []turnload.DiscoveryEntry) (*turnload.Discovery, error) {
	scope := tool + "\x00" + tctx.Identity.SessionID + "\x00" + tctx.Identity.Agent + "\x00" + tctx.Turn.TurnSurfaceID
	page, err := turnload.Discover(scope, need, cursor, status, failure, entries)
	if err != nil {
		return nil, toolrejection.RejectInvalidArguments("TOOL_ARGS_INVALID", map[string]any{"reason": err.Error()})
	}
	return page, nil
}
