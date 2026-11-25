package prompts_test

import (
	"strings"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/toolsurface"
)

// readerLoadedPlan promotes plan's loaded deferred tools.
func readerLoadedPlan(plan toolsurface.Plan, loaded map[string]bool) toolsurface.Plan {
	for name, ok := range loaded {
		if ok && plan.Deferred(name) {
			plan = plan.Promote(name)
		}
	}
	return plan
}

// readerLoadedOffer is the request offer on plan with loaded tools promoted:
// the visible names it makes immediate, or every immediate tool for nil.
func readerLoadedOffer(plan toolsurface.Plan, visible []string, loaded map[string]bool) []string {
	promoted := readerLoadedPlan(plan, loaded)
	if visible == nil {
		return promoted.ImmediateNames()
	}
	out := []string{}
	for _, name := range visible {
		if promoted.Immediate(name) {
			out = append(out, name)
		}
	}
	return out
}
