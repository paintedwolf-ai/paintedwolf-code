package review

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"log/slog"
)

// RecoverVerdictOperations replays pending verdicts; transient failures stay
// pending for the next boot.
func (m *Verdicts) RecoverVerdictOperations(ctx context.Context) error {
	if m == nil || m.Runs == nil {
		return nil
	}
	operations, err := m.Records.PendingVerdictOperations(ctx)
	if err != nil {
		return fmt.Errorf("list pending verdict operations: %w", err)
	}
	for _, op := range operations {
		var input runstate.VerdictSubmission
		if err := json.Unmarshal([]byte(op.EvidenceJSON), &input); err != nil {
			reason := "stored verdict input is invalid: " + err.Error()
			if resolveErr := m.Records.ResolveVerdictOperationDiverged(ctx, op.ToolCallID, reason); resolveErr != nil {
				slog.ErrorContext(ctx, "workflow verdict operation could not be resolved",
					"tool_call_id", op.ToolCallID, "err", resolveErr)
			}
			continue
		}
		if _, err := m.RecordReviewLoopVerdict(WithOperationID(ctx, op.ToolCallID), input.SessionID, input.Verdict, input.Cited, input.CitedURLs); err != nil {
			slog.WarnContext(ctx, "workflow verdict recovery deferred; row stays pending for the next boot",
				"tool_call_id", op.ToolCallID, "run_id", op.RunID, "phase", op.Phase, "err", err)
		}
	}
	return nil
}
