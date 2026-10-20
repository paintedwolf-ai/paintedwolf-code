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

// ProgressHandler returns the update_progress handler.
func ProgressHandler(store progress.Store, scopeKey ProgressScopeKey) tools.ToolHandler {
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
		result, err := prepared.Apply(ctx, store, root)
		if err != nil {
			return "", err
		}
		status := "unchanged"
		if result.Updated {
			status = "updated"
		}

		raw, _ := surveyjson.Marshal(map[string]any{"status": status})
		return string(raw), nil
	}
}
