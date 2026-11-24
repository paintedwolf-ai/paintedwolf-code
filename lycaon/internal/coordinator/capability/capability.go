package capability

import (
	"fmt"
	"slices"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/prompts"
)

// MergeForTurn derives roster role vars and skill visibility from the resolved per-turn capability surface.
func MergeForTurn(
	into map[string]any,
	reader prompts.Reader,
	spawnAllowlist []string,
) error {
	if into == nil {
		return fmt.Errorf("nil template vars map")
	}
	if !slices.Contains(reader.Offered(), "skills_read") {
		delete(into, "skills_roster")
		into["skills_count"] = 0
		into["has_skills"] = false
		delete(into, "skills_auto_loaded")
	}
	webSearchEnabled := reader.WebSearch()
	surf, err := Resolve(ResolveInput{
		Profile:          surface.TurnProfile{SurfaceID: reader.SurfaceID()},
		RootCount:        reader.RootCount(),
		SpawnAllowlist:   spawnAllowlist,
		WebSearchEnabled: &webSearchEnabled,
	})
	if err != nil {
		return err
	}
	for k, v := range prompts.SpawnRosterRoleVars(surf.Roster) {
		into[k] = v
	}
	return nil
}
