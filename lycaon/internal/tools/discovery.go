package tools

import (
	"context"
	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

// DiscoveryPage scopes cursors to the caller and its current permitted catalog.
func DiscoveryPage(ctx context.Context, tctx ToolContext, tool, need, cursor, status string, failure turnload.RankingFailure, entries []turnload.DiscoveryEntry, order ...string) (*turnload.Discovery, error) {
	scope := tool + "\x00" + tctx.Identity.SessionID + "\x00" + tctx.Identity.Agent + "\x00" + tctx.Turn.TurnSurfaceID
	instructions := turnload.DiscoveryInstructions{}
	for _, state := range []struct {
		total  int
		more   bool
		target *string
	}{
		{0, false, &instructions.Empty}, {len(entries), true, &instructions.More}, {len(entries), false, &instructions.Final},
	} {
		text, err := guidance.RenderGuidance(ctx, "utility/catalog-discovery", map[string]any{"total": state.total, "has_more": state.more})
		if err != nil {
			return nil, err
		}
		*state.target = text
	}
	page, err := turnload.Discover(scope, need, cursor, status, failure, entries, instructions, order...)
	if err != nil {
		return nil, toolrejection.RejectInvalidArguments("TOOL_ARGS_INVALID", map[string]any{"reason": err.Error()})
	}
	return page, nil
}
