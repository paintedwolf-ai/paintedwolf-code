package promptloop

import (
	"context"

	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/pkg/api"
)

// fitToolOutput is the one gate every tool result passes on its way to the
// model: a successful result past the spill cap that carries no truncation
// receipt is refused, and every other result is fitted to the session wire,
// whole, truncated with a receipt, or refused.
// ownerRef names the call whose spill an overlay promotion files under.
func (l *toolBatch) fitToolOutput(ctx context.Context, sess *api.Session, tool, ownerRef string, args map[string]any, content string, succeeded bool) toolResultProjection {
	maxBytes, maxSpillBytes := l.toolResultLimits(ctx, sess)
	if succeeded {
		if code, data := tooloutput.ClassifyEmitReject(tool, args, content, maxSpillBytes); code != "" {
			spillWireLog.Info("tool emit rejected", "code", code, "tool", tool, "bytes", data["bytes"], "cap", data["cap"])
			return toolResultProjection{reject: &toolrejection.ToolReject{Code: code, Data: data}}
		}
	}
	// Compact only after stamping evidence handles.
	storage := l.Projection.projectToolResultForStorage(ctx, content, args)
	return l.Tools.truncateToolResultForSession(ctx, tool, ownerRef, storage, content, maxBytes, maxSpillBytes, sess)
}
