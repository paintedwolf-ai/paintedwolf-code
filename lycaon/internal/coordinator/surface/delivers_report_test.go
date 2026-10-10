package surface_test

import (
	"github.com/lycaon/lycaon/internal/toolcontract"

	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/spawn"
)

func TestSurfaceDeliversReportMembership(t *testing.T) {
	// Both closeout surfaces deliver their report as a typed envelope, so the turn's
	// prose is provisional until commit projects the narrative out of it.
	for _, surfaceID := range []string{
		toolcontract.SurfaceImplementInvestigate,
		spawn.SurfaceImplementSynthesis,
	} {
		if !surface.SurfaceDeliversReport(surfaceID) {
			t.Fatalf("surface %q must be a coordinator closeout surface", surfaceID)
		}
	}
	// A non-closeout coordinator surface writes its orchestration prose directly.
	if surface.SurfaceDeliversReport("implement_dispatch") {
		t.Fatal("non-closeout surface must not be reported as a closeout surface")
	}
}
