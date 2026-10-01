package spawn

import (
	"testing"
)

func TestAmbientAllowedAgentsCopy(t *testing.T) {
	a := AmbientAllowedAgents()
	b := AmbientAllowedAgents()
	if len(a) == 0 {
		t.Fatal("expected non-empty allowlist from YAML")
	}
	a[0] = "mutated"
	if b[0] == "mutated" {
		t.Fatal("AmbientAllowedAgents must return a copy")
	}
}

// A lane-gated surface names a lane the vocabulary knows, so a typo in operator
// YAML fails the load rather than silently gating on nothing.
func TestImplementSpawnSurfaceLanesAreDeclared(t *testing.T) {
	cfg := bundledSpawnConfig()
	if len(cfg.SurfaceLanes) == 0 {
		t.Fatal("surface_lanes is empty — no coordinator surface gates a lane")
	}
	for surfaceID := range cfg.SurfaceLanes {
		if lane, gated := LaneForSurface(surfaceID); !gated || lane == "" {
			t.Fatalf("surface %q resolved to (%q,%v)", surfaceID, lane, gated)
		}
	}
}
