package tools

import (
	"context"

	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolsurface"
)

func (t requestTools) availability(ctx context.Context, tctx ToolContext, profileID string, plan toolsurface.Plan, constrained bool, name string) toolsurface.Availability {
	if _, registered := t.reg.Meta(name); !registered {
		return toolsurface.Unavailable
	}
	if plan.Addressable(name) {
		return plan.Availability(name)
	}
	if constrained && (plan.Compiled() || toolcontract.IsCatalog(name)) {
		return toolsurface.Unavailable
	}
	if t.deps.Boundary.ToolDeferred(profileID, name, tctx.Turn.ToolAccess) {
		return toolsurface.Deferred
	}
	if t.deps.Boundary.AssertToolAllowed(ctx, profileID, name, tctx.Turn.ToolAccess) == nil {
		return toolsurface.Immediate
	}
	return toolsurface.Unavailable
}
