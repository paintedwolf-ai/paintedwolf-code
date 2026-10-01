package spawn

import "testing"

func TestLaneForSurfaceReadsOperatorYAML(t *testing.T) {
	lane, gated := LaneForSurface(SurfaceImplementRouting)
	if !gated || lane != "read" {
		t.Fatalf("routing lane = (%q,%v) want (read,true)", lane, gated)
	}
	lane, gated = LaneForSurface(SurfaceImplementDispatch)
	if !gated || lane != "write" {
		t.Fatalf("dispatch lane = (%q,%v) want (write,true)", lane, gated)
	}
}

// A surface with no row gates on nothing, so it admits whatever a workflow
// declared.
func TestLaneForSurfaceUngatedSurfaces(t *testing.T) {
	for _, surfaceID := range []string{SurfaceImplementSynthesis, "implement_investigate", "orchestrate_plan", ""} {
		if lane, gated := LaneForSurface(surfaceID); gated {
			t.Fatalf("surface %q unexpectedly gates on lane %q", surfaceID, lane)
		}
	}
}
