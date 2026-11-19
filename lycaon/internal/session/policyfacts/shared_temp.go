package policyfacts

import (
	"context"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/pkg/api"
)

func (m *Service) sharedTempResult(ctx context.Context, sess *api.Session, tool string, args map[string]any, output string, result guidance.SharedTempSettlement) (string, guidance.ToolResultFacts) {
	gc := oar.NewGuardContext()
	m.FillSessionFacts(ctx, gc, sess, tool, args)
	gc.Execution.ProcessRunning = result.Running
	data := map[string]any{"refused_shared_temp": result.Refused, "shared_temp_allowed": result.Allowed, "shared_temp_declined": result.Declined, "shared_temp_unanswered": result.Unanswered, "shared_temp_tolerated": result.Tolerated, "session_scratch_available": result.ScratchAvailable}
	gc.PutRejectData("SANDBOX_SHARED_TEMP_REFUSED", data)
	gc.PutRejectData("SANDBOX_SHARED_TEMP_TOLERATED", data)
	res, err := m.Pipeline.EvaluateBlock(ctx, oar.AnchorSharedTemp, gc)
	if err != nil {
		return "", guidance.ToolResultFacts{ContentReplaced: true, Outcome: api.ToolResultOutcomeError}
	}
	return m.feedback.DeliverPostToolResult(ctx, oar.AnchorSharedTemp, output, res)
}
