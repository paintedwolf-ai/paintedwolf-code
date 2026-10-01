package prompts_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/prompts"
)

func TestCoordinatorRosterFollowsTheTurnSurface(t *testing.T) {
	roster := []map[string]any{{"name": "reach-a-network-service", "description": "…"}}

	investigate := map[string]any{"agent_skills": roster}
	if err := prompts.MergeCoordinatorSurfacePathVars("implement_investigate", nil, investigate, prompts.SurfaceTurn{}); err != nil {
		t.Fatalf("merge investigate surface: %v", err)
	}
	if readable, _ := investigate["profile_has_skills_read"].(bool); !readable {
		t.Fatal("implement_investigate carries skills_read; the flag must say so")
	}
	if _, present := investigate["agent_skills"]; !present {
		t.Fatal("a turn that can read skills must keep its roster")
	}

	for _, surfaceID := range []string{"implement_routing", "implement_dispatch", "implement_synthesis"} {
		vars := map[string]any{"agent_skills": roster}
		if err := prompts.MergeCoordinatorSurfacePathVars(surfaceID, nil, vars, prompts.SurfaceTurn{}); err != nil {
			t.Fatalf("merge %s: %v", surfaceID, err)
		}
		if readable, _ := vars["profile_has_skills_read"].(bool); readable {
			t.Fatalf("%s does not carry skills_read; flag must not claim it", surfaceID)
		}
		if _, present := vars["agent_skills"]; present {
			t.Fatalf("%s cannot open a skill, so it must advertise none", surfaceID)
		}
	}
}
