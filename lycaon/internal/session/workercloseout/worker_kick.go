package workercloseout

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
)

// WorkerKickRenderer renders host kick templates (kicks/{id}.md).
type WorkerKickRenderer func(ctx context.Context, kickID string, data map[string]any) (string, error)

// RenderWorkerKick renders a catalog kick; when the template engine is missing
// or fails, a registry-known stem degrades to its "[host:<stem>]" marker so the
// turn still carries the signal.
func RenderWorkerKick(ctx context.Context, render WorkerKickRenderer, kickID string, data map[string]any) string {
	kickID = strings.TrimSpace(kickID)
	if kickID == "" {
		return ""
	}
	if render != nil {
		if kick, err := render(ctx, kickID, data); err == nil && strings.TrimSpace(kick) != "" {
			return strings.TrimSpace(kick)
		}
	}
	if _, ok := anchor.ParseID(kickID); ok {
		return "[host:" + kickID + "]"
	}
	return ""
}
