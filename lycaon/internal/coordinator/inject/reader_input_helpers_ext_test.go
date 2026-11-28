package inject_test

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

// readerDeclaredOffer offers the visible names, or every profile tool for nil,
// that the profile does not defer or that loaded names.
func readerDeclaredOffer(profiles []sandbox.ToolProfile, profileID string, visible []string, loaded map[string]bool) []string {
	var profile sandbox.ToolProfile
	for _, p := range profiles {
		if p.ID == profileID {
			profile = p
		}
	}
	names := visible
	if names == nil {
		for name, ok := range profile.Tools {
			if ok && !strings.HasSuffix(name, "*") {
				names = append(names, name)
			}
		}
	}
	out := []string{}
	for _, name := range names {
		if !profile.ToolDeferred(name) || loaded[name] {
			out = append(out, name)
		}
	}
	return out
}
