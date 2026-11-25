package workercloseout

import (
	"context"
	"log/slog"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/prompts"
)

// WorkerKickRenderer renders host kick templates (kicks/{id}.md) against a validated Reader.
type WorkerKickRenderer func(ctx context.Context, reader prompts.Reader, kickID string, data map[string]any) (string, error)

// RenderWorkerKick renders a catalog kick; without a renderer or a valid
// Reader, or when rendering fails, a registry-known stem degrades to its
// "[host:<stem>]" marker so the turn still carries the signal.
func RenderWorkerKick(ctx context.Context, render WorkerKickRenderer, reader prompts.Reader, kickID string, data map[string]any) string {
	kickID = strings.TrimSpace(kickID)
	if kickID == "" {
		return ""
	}
	switch {
	case render == nil:
	case !reader.Valid():
		slog.WarnContext(ctx, "kick has no request reader", "kick", kickID)
	default:
		kick, err := render(ctx, reader, kickID, data)
		if err == nil && strings.TrimSpace(kick) != "" {
			return strings.TrimSpace(kick)
		}
		slog.WarnContext(ctx, "kick render", "kick", kickID, "error", err)
	}
	if _, ok := anchor.ParseID(kickID); ok {
		return "[host:" + kickID + "]"
	}
	return ""
}
