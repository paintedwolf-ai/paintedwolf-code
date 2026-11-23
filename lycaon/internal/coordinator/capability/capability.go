package capability

import (
	"fmt"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/agentdef"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/prompts"
)

// Orientation tools in project roots.
const (
	orientToolBoard   = "pack_board"
	orientToolListDir = "list_dir"
)

// CoordinatorCapabilities are derived each turn from root_count and the resolved tool/spawn surfaces.
type CoordinatorCapabilities struct {
	RootCount           int
	HasWorkspaceRoots   bool
	HasFileTools        bool
	CanOrient           bool
	CanSpawnImplementer bool
	CanSpawnWebResearch bool
	CanSpawnWorkers     bool
}

// Derive computes capabilities from the current tool and worker rosters.
func Derive(rootCount int, surfaceTools, spawnAllowlist []string) CoordinatorCapabilities {
	surfaceSet := toolNameSet(surfaceTools)
	spawnSet := toolNameSet(spawnAllowlist)
	caps := CoordinatorCapabilities{
		RootCount:         rootCount,
		HasWorkspaceRoots: rootCount > 0,
	}
	if rootCount > 0 {
		for name := range surfaceSet {
			if prompts.IsProjectFileTool(name) {
				caps.HasFileTools = true
				break
			}
		}
	}
	if surfaceSet[orientToolBoard] || surfaceSet[orientToolListDir] {
		caps.CanOrient = true
	}
	// Worker capabilities include installed packs.
	for name := range spawnSet {
		if capable, ok := prompts.AgentMutationCapable(name); ok && capable {
			caps.CanSpawnImplementer = true
		}
		if agentdef.DeclaresAny(name, agentdef.CapabilityExternal) {
			caps.CanSpawnWebResearch = true
		}
	}
	caps.CanSpawnWorkers = caps.CanSpawnImplementer || caps.CanSpawnWebResearch || len(spawnSet) > 0
	return caps
}

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

func toolNameSet(names []string) map[string]bool {
	out := make(map[string]bool, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name != "" {
			out[name] = true
		}
	}
	return out
}
