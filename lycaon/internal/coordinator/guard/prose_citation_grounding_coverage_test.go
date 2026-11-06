package guard_test

import (
	"github.com/lycaon/lycaon/internal/toolcontract"

	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/spawn"
	"gopkg.in/yaml.v3"
)

// proseGroundingSurfaceUniverse is every implement-family coordinator surface a host turn can
// land on: the runtime surface keys plus the code-defined surface consts. The closure assertion
// fails when a runtime surface is added without extending this list.
func proseGroundingSurfaceUniverse(t *testing.T) []string {
	t.Helper()
	known := map[string]struct{}{
		spawn.SurfaceImplementSynthesis:          {},
		spawn.SurfaceImplementRouting:            {},
		surface.SurfaceImplementDispatch:         {},
		surface.SurfaceImplementOverlayPromote:   {},
		surface.SurfaceImplementPark:             {},
		toolcontract.SurfaceImplementInvestigate: {},
	}

	data, err := os.ReadFile(filepath.Join("..", "..", "..", "config", "packs", "painted-wolf", "platform", "host", "coordinator-surfaces.yaml"))
	if err != nil {
		t.Fatalf("read coordinator-surfaces.yaml: %v", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse coordinator-surfaces.yaml: %v", err)
	}
	for key := range doc {
		if len(key) < len("implement_") || key[:len("implement_")] != "implement_" {
			continue
		}
		if _, ok := known[key]; !ok {
			t.Fatalf("coordinator-surfaces.yaml defines implement surface %q absent from the prose-grounding coverage universe — add it so its grounding is checked", key)
		}
	}

	out := make([]string, 0, len(known))
	for s := range known {
		out = append(out, s)
	}
	return out
}

// Prose completion and grounding share one surface predicate.
func TestProseFinishSurfacesAreAlwaysGrounded(t *testing.T) {
	t.Parallel()

	groundedProse := 0
	for _, surfaceID := range proseGroundingSurfaceUniverse(t) {
		if guard.SurfaceFinishesWithUserProse(surfaceID) {
			groundedProse++
		}
	}
	if groundedProse == 0 {
		t.Fatal("no prose-finish surface is grounded — the coverage property is checking nothing")
	}

	// Tool-based completion is outside the prose predicate.
	if guard.SurfaceFinishesWithUserProse(surface.SurfaceImplementOverlayPromote) {
		t.Fatal("overlay-promote closes via a tool, not user prose — it must not be a citation-grounding surface")
	}
}
