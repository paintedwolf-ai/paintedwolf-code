package prompts_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/prompts"
)

func TestCoordinatorRosterFollowsTheTurnSurface(t *testing.T) {
	roster := []map[string]any{{"name": "reach-a-network-service", "description": "…"}}

	reader, err := prompts.DeclaredReader(nil, "implement_investigate")
	if err != nil {
		t.Fatalf("declared reader investigate: %v", err)
	}
	investigate, err := prompts.Vars(reader, map[string]any{"agent_skills": roster})
	if err != nil {
		t.Fatalf("merge investigate surface: %v", err)
	}
	if readable, _ := investigate["profile_has_skills_read"].(bool); !readable {
		t.Fatal("implement_investigate carries skills_read; the flag must say so")
	}
	if _, present := investigate["agent_skills"]; !present {
		t.Fatal("a turn that can read skills must keep its roster")
	}

	for _, surfaceID := range []string{"implement_routing", "implement_dispatch", "implement_synthesis"} {
		r, err := prompts.DeclaredReader(nil, surfaceID)
		if err != nil {
			t.Fatalf("declared reader %s: %v", surfaceID, err)
		}
		vars, err := prompts.Vars(r, map[string]any{"agent_skills": roster})
		if err != nil {
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
