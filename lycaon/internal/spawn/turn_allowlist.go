package spawn

import "strings"

const (
	// SurfaceImplementRouting is the coordinator capability surface for scout chaining.
	SurfaceImplementRouting = "implement_routing"
	// SurfaceImplementDispatch is write/verify dispatch after read scouts (per-job wake).
	SurfaceImplementDispatch = "implement_dispatch"
	// SurfaceImplementSynthesis is the coordinator capability surface for Lane S + write dispatch.
	SurfaceImplementSynthesis = "implement_synthesis"
)

// LaneForSurface returns the dispatch lane a coordinator surface gates on, and
// whether it gates at all. The mapping is operator YAML and names no agent: who
// serves a lane is each agent's own `dispatch_lanes` declaration, so admitting a
// new agent never means editing this pack.
func LaneForSurface(surfaceID string) (string, bool) {
	lane, ok := bundledSpawnConfig().SurfaceLanes[strings.TrimSpace(surfaceID)]
	if !ok {
		return "", false
	}
	lane = strings.TrimSpace(lane)
	return lane, lane != ""
}
