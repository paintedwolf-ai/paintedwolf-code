package workercontrol

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
)

// CompleteLegTool is the worker closeout tool name.
const CompleteLegTool = "complete_leg"

// CompleteLegRecord is the accepted-report view the closeout ack states.
type CompleteLegRecord struct {
	LegStatus string
	Findings  int
}

// CompleteLegDecoder validates the report stored with an accepted call.
type CompleteLegDecoder func(args map[string]any) (CompleteLegRecord, bool)

func CompleteLegHandler(decode CompleteLegDecoder) tools.ToolHandler {
	return func(_ context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		if tools.OutOfSessionScope(CompleteLegTool, tctx) {
			return "", &tools.ToolReject{
				Code: "COMPLETE_LEG_ADDRESSED_SESSION",
				Data: map[string]any{"tool": CompleteLegTool},
			}
		}
		record, ok := decode(args)
		if !ok {
			return "", &tools.ToolReject{
				Code: "COMPLETE_LEG_STATUS_REQUIRED",
				Data: map[string]any{"tool": CompleteLegTool},
			}
		}
		raw, err := surveyjson.Marshal(map[string]any{
			"recorded":          true,
			"leg_status":        record.LegStatus,
			"findings_recorded": record.Findings,
		})
		if err != nil {
			return "", fmt.Errorf("encode complete_leg ack: %w", err)
		}
		return string(raw), nil
	}
}
