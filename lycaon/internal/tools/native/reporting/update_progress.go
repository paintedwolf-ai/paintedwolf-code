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

// ProgressHandler returns the update_progress handler.
func ProgressHandler(store progress.Store, scopeKey ProgressScopeKey, reconciliation progress.Reconciler) tools.ToolHandler {
	return func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		root := strings.TrimSpace(scopeKey(ctx, tctx.Identity.SessionID))
		if root == "" {
			return "", fmt.Errorf("session required")
		}
		content, _ := args["content"].(string)
		prepared, err := progress.PrepareAuthorUpdate(content)
		if err != nil {
			var rejected *progress.AuthorUpdateRejection
			if errors.As(err, &rejected) {
				return "", &toolrejection.ToolReject{Code: rejected.Code, Data: rejected.Data}
			}
			return "", err
		}
		expected, _ := args["expected_revision"].(string)
		token, _ := args["reconciliation_token"].(string)
		result, err := prepared.ExpectRevision(expected).ExpectReconciliation(token).Apply(ctx, store, root, reconciliation)
		if err != nil {
			var rejected *progress.AuthorUpdateRejection
			if errors.As(err, &rejected) {
				return "", &toolrejection.ToolReject{Code: rejected.Code, Data: rejected.Data}
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

		raw, _ := surveyjson.Marshal(map[string]any{"status": status, "document_changed": result.Updated, "state_changed": result.StateChanged, "deliverable_closed": result.DeliverableClosed, "reconciliation_status": result.ReconciliationStatus, "snapshot": result.Snapshot})
		return string(raw), nil
	}
}
