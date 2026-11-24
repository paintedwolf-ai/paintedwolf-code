package reporting

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
)

// ProgressScopeKey resolves the visible root session.
type ProgressScopeKey func(ctx context.Context, sessionID string) string

// ProgressHandler returns the row-DML update_progress handler.
func ProgressHandler(store progress.Store, scopeKey ProgressScopeKey, reconciliation progress.TxReconciler) tools.ToolHandler {
	return func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		root := strings.TrimSpace(scopeKey(ctx, tctx.Identity.SessionID))
		if root == "" {
			return "", fmt.Errorf("session required")
		}
		mutation, err := progress.PrepareMutation(args)
		if err != nil {
			var rejected *progress.MutationRejection
			if errors.As(err, &rejected) {
				return "", &toolrejection.ToolReject{Code: rejected.Code, Data: rejected.Data, BeforeEffect: true}
			}
			return "", &toolrejection.ToolReject{
				Code:         "TOOL_ARGS_INVALID",
				Data:         map[string]any{"tool": "update_progress", "reason": err.Error()},
				BeforeEffect: true,
			}
		}
		result, err := store.Apply(ctx, root, mutation, progress.ApplyOptions{
			PrincipalID: root,
			Reconciler:  reconciliation,
		})
		if err != nil {
			var rejected *progress.MutationRejection
			if errors.As(err, &rejected) {
				if snapshot, ok := rejected.Data["snapshot"].(progress.Snapshot); ok {
					rejected.Data["snapshot"] = progress.ForRequest(root, snapshot)
				}
				return "", &toolrejection.ToolReject{Code: rejected.Code, Data: rejected.Data, BeforeEffect: true}
			}
			return "", err
		}
		if tctx.Effects.Out != nil {
			advanced := result.AdvancesWorkflow()
			tctx.Effects.Out.Facts.WorkflowAdvanced = &advanced
		}
		status := "unchanged"
		if result.Updated {
			status = "updated"
		}
		acked := mutation.AcknowledgeResults
		if acked == nil {
			acked = []string{}
		}
		raw, _ := surveyjson.Marshal(map[string]any{
			"status":                status,
			"document_changed":      result.Updated,
			"state_changed":         result.StateChanged,
			"added_item_ids":        result.AddedItemIDs,
			"closed_item_ids":       result.ClosedItemIDs,
			"removed_item_ids":      result.RemovedItemIDs,
			"acknowledged_results":  acked,
			"reconciliation_status": result.ReconciliationStatus,
			"snapshot":              progress.ForRequest(root, result.Snapshot),
		})
		return string(raw), nil
	}
}
