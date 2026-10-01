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
	profile surface.TurnProfile,
	rootCount int,
	spawnAllowlist []string,
	webSearchEnabled bool,
) error {
	if into == nil {
		return fmt.Errorf("nil template vars map")
	}
	surf, err := Resolve(ResolveInput{
		Profile:          profile,
		RootCount:        rootCount,
		SpawnAllowlist:   spawnAllowlist,
		WebSearchEnabled: &webSearchEnabled,
	})
	if err != nil {
		return err
	}
	MergeVars(into, surf.Flags)
	for k, v := range prompts.CoordinatorPolicyTemplateVars(surf.ResolvedTools, surf.DeferredTools) {
		into[k] = v
	}
	label, rule, err := prompts.LoadCoordinatorSurfaceCard(surf.SurfaceID)
	if err != nil {
		return err
	}
	for k, v := range prompts.CoordinatorSurfaceCardVars(label, rule, surf.ResolvedTools, surf.DeferredTools).TemplateVars() {
		into[k] = v
	}
	prompts.MergeVisualShowVars(surf.ResolvedTools, into)
	into["surface_offered"] = append([]string(nil), surf.ResolvedTools...)
	for k, v := range prompts.SpawnRosterRoleVars(surf.Roster) {
		into[k] = v
	}
	into["web_search_enabled"] = webSearchEnabled
	if !webSearchEnabled {
		prompts.MergeWebResearchUnavailable(into)
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
