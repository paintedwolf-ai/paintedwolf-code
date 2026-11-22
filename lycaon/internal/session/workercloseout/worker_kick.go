package workercloseout

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/prompts"
)

// WorkerKickRenderer renders host kick templates (kicks/{id}.md) against a validated Reader.
type WorkerKickRenderer func(ctx context.Context, reader prompts.Reader, kickID string, data map[string]any) (string, error)

// RenderWorkerKick renders a catalog kick; when the template engine is missing
// or fails, a registry-known stem degrades to its "[host:<stem>]" marker so the
// turn still carries the signal.
func RenderWorkerKick(ctx context.Context, render WorkerKickRenderer, reader prompts.Reader, kickID string, data map[string]any) string {
	kickID = strings.TrimSpace(kickID)
	if kickID == "" {
		return ""
	}
	if render != nil && reader.Valid() {
		if kick, err := render(ctx, reader, kickID, data); err == nil && strings.TrimSpace(kick) != "" {
			return strings.TrimSpace(kick)
		}
	}
	if _, ok := anchor.ParseID(kickID); ok {
		return "[host:" + kickID + "]"
	}
	return ""
}
