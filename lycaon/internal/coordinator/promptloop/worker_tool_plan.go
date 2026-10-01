package promptloop

import (
	"context"

	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolsurface"
	"github.com/lycaon/lycaon/internal/webresearch"
	"github.com/lycaon/lycaon/pkg/api"
)

// compileWorkerToolPlan starts from profile-filtered capabilities, never the coordinator roster.
func (l *PromptLoop) compileWorkerToolPlan(ctx context.Context, sess *api.Session, metas []tools.ToolMeta, activated map[string]bool) toolsurface.Plan {
	mcp := l.mcpToolPlan(ctx, sess, metas, activated)
	var immediate, deferred []string
	for _, meta := range metas {
		if !l.webSearchEnabled() && (meta.Name == webresearch.SearchToolName || meta.Name == webresearch.FetchURLToolName) {
			continue
		}
		eager := !meta.Deferred
		if meta.IsMCP() {
			eager = mcp.Eager(meta.Name)
		}
		if eager {
			immediate = append(immediate, meta.Name)
		} else {
			deferred = append(deferred, meta.Name)
		}
	}
	plan := toolsurface.Compile(immediate, deferred)
	for name := range activated {
		if plan.Addressable(name) {
			plan = plan.Promote(name)
		}
	}
	for _, name := range toolcontract.Implied(l.liveResources(sess)) {
		if plan.Addressable(name) {
			plan = plan.Promote(name)
		}
	}
	return plan
}
