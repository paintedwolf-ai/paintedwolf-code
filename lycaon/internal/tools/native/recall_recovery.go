package native

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/recall"
)

func addRecallRecoveryAction(ctx context.Context, result *recall.Result) {
	ref := ""
	switch {
	case result.NextRequest != nil:
		ref = "recall/history-continuation"
	case result.History != nil && result.History.Format == "index" && len(result.History.Entries) > 0:
		ref = "recall/record-request"
	default:
		for _, hit := range result.Hits {
			if hit.Record != nil && len(hit.Body) == 0 {
				ref = "recall/record-request"
				break
			}
		}
	}
	if ref == "" {
		return
	}
	if action, err := guidance.RenderGuidance(ctx, ref, nil); err == nil {
		result.NextAction = strings.TrimSpace(result.NextAction + "\n" + action)
	}
}

func recallCoordinateRejectFacts(err *recall.ErrHistoryCoordinates) map[string]any {
	facts := map[string]any{"field": err.Field, "reason": err.Reason}
	switch err.Field {
	case "history":
		facts["recall_history_mode_invalid"] = true
	case "history.through_ord", "history.after_ord":
		facts["recall_history_bounds_invalid"] = true
	case "history.message_id":
		facts["recall_history_body_missing"] = true
	case "history.search":
		facts["recall_history_search_invalid"] = true
	}
	return facts
}
