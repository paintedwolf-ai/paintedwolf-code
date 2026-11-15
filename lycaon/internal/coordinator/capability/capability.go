package capability

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/agentdef"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/toolscope"
)

// Orientation tools in project roots.
const (
	orientToolBoard   = "pack_board"
	orientToolListDir = "list_dir"
)

// CoordinatorCapabilities are derived each turn from root_count and the resolved tool/spawn surfaces.
type CoordinatorCapabilities struct {
	RootCount           int
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
	caps := CoordinatorCapabilities{RootCount: rootCount}
	for name := range surfaceSet {
		if toolscope.RequiresProjectRoots(name) {
			caps.HasFileTools = true
			break
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

// MergeVars adds capability pongo vars for coordinator partials.
func MergeVars(into map[string]any, caps CoordinatorCapabilities) {
	if into == nil {
		return
	}
	into["has_file_tools"] = caps.HasFileTools
	into["can_orient"] = caps.CanOrient
	into["can_spawn_implementer"] = caps.CanSpawnImplementer
	into["can_spawn_web_research"] = caps.CanSpawnWebResearch
	into["can_spawn_workers"] = caps.CanSpawnWorkers
	if _, ok := into["root_count"]; !ok {
		into["root_count"] = caps.RootCount
	}
}

// MergeForTurn derives capability flags from the resolved per-turn capability surface.
func MergeForTurn(
	into map[string]any,
	reader prompts.Reader,
	spawnAllowlist []string,
) error {
	if into == nil {
		return fmt.Errorf("nil template vars map")
	}
	if _, hasOffered := into["surface_offered"]; !hasOffered {
		for k, v := range reader.Facts() {
			if _, exists := into[k]; !exists {
				into[k] = v
			}
		}
	}
	webSearchEnabled := reader.WebSearch()
	surf, err := Resolve(ResolveInput{
		Profile:          surface.TurnProfile{SurfaceID: reader.SurfaceID()},
		RootCount:        reader.RootCount(),
		SpawnAllowlist:   spawnAllowlist,
		WebSearchEnabled: &webSearchEnabled,
		Catalog:          reader.Catalog(),
		ToolProfiles:     reader.Profiles(),
	})
	if err != nil {
		return err
	}
	MergeVars(into, surf.Flags)
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
