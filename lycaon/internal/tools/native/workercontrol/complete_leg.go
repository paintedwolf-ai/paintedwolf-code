package workercontrol

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"

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
type CompleteLegDecoder func(context.Context, map[string]any, tools.ToolContext) (CompleteLegRecord, error)

func CompleteLegHandler(decode CompleteLegDecoder) tools.ToolHandler {
	return func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		if tools.OutOfSessionScope(CompleteLegTool, tctx) {
			return "", &toolrejection.ToolReject{
				Code: "COMPLETE_LEG_ADDRESSED_SESSION",
				Data: map[string]any{"tool": CompleteLegTool},
			}
		}
		record, err := decode(ctx, args, tctx)
		if err != nil {
			return "", err
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
