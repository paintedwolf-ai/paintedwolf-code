package reporting

import (
	"context"
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
		root := strings.TrimSpace(scopeKey(ctx, tctx.SessionID))
		if root == "" {
			return "", fmt.Errorf("session required")
		}
		content, _ := args["content"].(string)
		content = strings.TrimSpace(content)
		if content == "" {
			return "", fmt.Errorf("content is required")
		}
		if code, data, ok := progress.ValidateAuthorProgress(content); !ok {
			return "", &toolrejection.ToolReject{Code: code, Data: data}
		}
		prev := store.Get(ctx, root)
		// An identical checklist writes nothing, so a replay adds no revision or
		// transcript row. already_open says the progress gate was open before this call.
		if strings.TrimSpace(prev) == content {
			raw, _ := surveyjson.Marshal(map[string]any{
				"status":     "unchanged",
				"gate_state": "already_open",
			})
			return string(raw), nil
		}
		if err := store.Set(root, content); err != nil {
			return "", fmt.Errorf("store progress: %w", err)
		}
		progress.NotifyWriteObservers(ctx, progress.WriteEvent{SessionID: root, Prev: prev})

		raw, _ := surveyjson.Marshal(map[string]any{"status": "updated"})
		return string(raw), nil
	}
}
