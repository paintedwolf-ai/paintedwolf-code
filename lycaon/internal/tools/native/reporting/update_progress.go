package reporting

import (
	"context"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"strings"

	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
)

// ProgressScopeKey resolves the visible root session.
type ProgressScopeKey func(ctx context.Context, sessionID string) string

type ProgressReconciler interface {
	Reconciliation(string) *progress.Reconciliation
	Acknowledge(string, string) bool
}

// ProgressHandler returns the update_progress handler.
func ProgressHandler(store progress.Store, scopeKey ProgressScopeKey, reconciliation ProgressReconciler) tools.ToolHandler {
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
		if token != "" {
			var pending *progress.Reconciliation
			if reconciliation != nil {
				pending = reconciliation.Reconciliation(root)
			}
			if expected == "" || pending == nil || pending.Token != token {
				current, readErr := store.Snapshot(ctx, root)
				if readErr != nil {
					return "", readErr
				}
				current.Reconciliation = pending
				return "", &toolrejection.ToolReject{Code: "PROGRESS_RECONCILIATION_CHANGED", Data: map[string]any{"snapshot": current}}
			}
		}
		result, err := prepared.ExpectRevision(expected).Apply(ctx, store, root)
		if err != nil {
			var rejected *progress.AuthorUpdateRejection
			if errors.As(err, &rejected) {
				if rejected.Code == "PROGRESS_REVISION_CHANGED" && reconciliation != nil {
					result.Snapshot.Reconciliation = reconciliation.Reconciliation(root)
					rejected.Data["snapshot"] = result.Snapshot
				}
				return "", &toolrejection.ToolReject{Code: rejected.Code, Data: rejected.Data}
			}
			return "", err
		}
		acknowledgement := "not_requested"
		if token != "" {
			acknowledgement = "changed"
			if reconciliation.Acknowledge(root, token) || result.DeliverableClosed && reconciliation.Reconciliation(root) == nil {
				acknowledgement = "acknowledged"
			}
		}
		if reconciliation != nil {
			result.Snapshot.Reconciliation = reconciliation.Reconciliation(root)
		}
		status := "unchanged"
		if result.Updated {
			status = "updated"
		}

		raw, _ := surveyjson.Marshal(map[string]any{"status": status, "document_changed": result.Updated, "state_changed": result.StateChanged, "deliverable_closed": result.DeliverableClosed, "reconciliation_status": acknowledgement, "snapshot": result.Snapshot})
		return string(raw), nil
	}
}
