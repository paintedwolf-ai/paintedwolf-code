package tools

import (
	"context"

	"github.com/lycaon/lycaon/internal/sandbox"
)

// SandboxScopeContext attaches session id and scope template tokens for path-scope checks.
func SandboxScopeContext(ctx context.Context, tc ToolContext) context.Context {
	ctx = sandbox.WithSessionID(ctx, tc.SessionID)
	ctx = sandbox.WithScopeTokens(ctx, sandbox.ScopeTokens{
		Self: tc.Agent,
		Job:  tc.WorkerJobID,
	})
	if len(tc.TurnWritePinGlobs) > 0 {
		pin := sandbox.TurnWritePin{Globs: append([]string(nil), tc.TurnWritePinGlobs...)}
		for _, root := range tc.Roots {
			if root.ID == tc.TurnWritePinRootID {
				pin.RootPath = root.Path
				break
			}
		}
		// An unresolved root leaves RootPath empty, which denies all writes.
		ctx = sandbox.WithTurnWritePin(ctx, pin)
	}
	return ctx
}
