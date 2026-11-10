package workercontrol

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"

	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"github.com/lycaon/lycaon/pkg/api"
)

// CompleteLegTool is the worker closeout tool name.
const CompleteLegTool = "complete_leg"

// CompleteLegDecoder accepts a report against the live worker context.
type CompleteLegDecoder func(context.Context, map[string]any, tools.ToolContext) (*api.WorkerReportAcceptance, error)

func CompleteLegHandler(decode CompleteLegDecoder) tools.ToolHandler {
	return func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		if tools.OutOfSessionScope(CompleteLegTool, tctx) {
			return "", &toolrejection.ToolReject{
				Code: "COMPLETE_LEG_ADDRESSED_SESSION",
				Data: map[string]any{"tool": CompleteLegTool},
			}
		}
		accepted, err := decode(ctx, args, tctx)
		if err != nil {
			return "", err
		}

		if accepted == nil || accepted.Report == nil || accepted.Grounding == nil || accepted.WorkerJobID == "" || accepted.WorkerJobID != tctx.Identity.WorkerJobID {
			return "", fmt.Errorf("complete_leg acceptance does not match worker job")
		}
		if tctx.Effects.Out == nil {
			return "", fmt.Errorf("complete_leg acceptance capture unavailable")
		}

		raw, err := surveyjson.Marshal(map[string]any{
			"recorded":          true,
			"leg_status":        accepted.Report.LegStatus,
			"findings_recorded": len(accepted.Report.Findings),
		})
		if err != nil {
			return "", fmt.Errorf("encode complete_leg ack: %w", err)
		}
		tctx.Effects.Out.WorkerReport = accepted
		return string(raw), nil
	}
}
