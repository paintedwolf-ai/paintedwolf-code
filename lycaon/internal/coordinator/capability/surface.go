package capability

import (
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/agentdef"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/webresearch"
)

const coordinatorAgentID = "coordinator"

// CapabilitySurface contains the tools and worker roster available for one turn.
type CapabilitySurface struct {
	RootCount      int
	RepoKnownEmpty bool
	SurfaceID      string
	ResolvedTools  []string
	DeferredTools  []string
	// SpawnAllowlist contains the frame’s effective worker roster.
	SpawnAllowlist      []string
	Flags               CoordinatorCapabilities
	Roster              prompts.SpawnRosterData
	ExcludedDisclosures []prompts.SpawnAgentView
}

// ResolveInput configures per-turn capability surface resolution.
type ResolveInput struct {
	Profile        surface.TurnProfile
	RootCount      int
	RepoKnownEmpty bool
	// SpawnAllowlist is the frame roster's effective agent set.
	SpawnAllowlist   []string
	MaxInFlight      int
	WebSearchEnabled *bool
	// Catalog and ToolProfiles scope the turn's roster.
	Catalog      *extpacks.EffectiveCatalog
	ToolProfiles []sandbox.ToolProfile
	Resources    toolcontract.ResourcePresence
}

// Resolve builds the single per-turn capability surface after all gating.
func Resolve(input ResolveInput) (CapabilitySurface, error) {
	surfaceID := strings.TrimSpace(input.Profile.SurfaceID)
	toolPlan, err := surface.CompileToolPlan(input.Profile, input.RootCount)
	if err != nil {
		return CapabilitySurface{}, err
	}
	resolvedTools := toolPlan.ImmediateNames()
	deferredTools := toolPlan.DeferredNames()
	searchEnabled := true
	if input.WebSearchEnabled != nil {
		searchEnabled = *input.WebSearchEnabled
	}
	if !searchEnabled {
		resolvedTools = webresearch.FilterSearchTool(resolvedTools)
		deferredTools = webresearch.FilterSearchTool(deferredTools)
	}
	resolvedTools = toolcontract.AppendImplied(resolvedTools, input.Resources)
	spawnAllowlist := append([]string(nil), input.SpawnAllowlist...)
	flags := Derive(input.RootCount, resolvedTools, spawnAllowlist)

	surfaceToolOverride := coordinatorSurfaceToolOverride(spawnAllowlist, resolvedTools)
	maxInFlight := input.MaxInFlight
	if maxInFlight <= 0 {
		maxInFlight = spawn.MaxInFlightTaskWorkers
	}
	roster, err := prompts.LoadSpawnRosterSurface(spawnAllowlist, maxInFlight, surfaceToolOverride, input.Catalog, input.ToolProfiles)
	if err != nil {
		return CapabilitySurface{}, err
	}
	registry, err := LoadDisclosureRegistry()
	if err != nil {
		return CapabilitySurface{}, err
	}
	excluded := FilterAgentsForDisclosure(roster.NotSpawnableAgents, registry)
	if !searchEnabled {
		excluded = filterExternalSourceAgentViews(excluded)
	}

	return CapabilitySurface{
		RootCount:           input.RootCount,
		RepoKnownEmpty:      input.RepoKnownEmpty,
		SurfaceID:           surfaceID,
		ResolvedTools:       append([]string(nil), resolvedTools...),
		DeferredTools:       append([]string(nil), deferredTools...),
		SpawnAllowlist:      spawnAllowlist,
		Flags:               flags,
		Roster:              roster,
		ExcludedDisclosures: excluded,
	}, nil
}

// MergeSpawnInjectVars returns pongo vars for inject/implement-spawn.md from the surface.
func (s CapabilitySurface) MergeSpawnInjectVars() map[string]any {
	vars := prompts.SpawnRosterTemplateVars(s.Roster, s.ExcludedDisclosures)
	vars["surface_id"] = s.SurfaceID
	vars["execution_mode"] = surface.ExecutionModeFamily(s.SurfaceID)
	for key, value := range prompts.CoordinatorPolicyTemplateVars(s.ResolvedTools, s.DeferredTools) {
		vars[key] = value
	}
	vars["repo_known_empty"] = s.RepoKnownEmpty
	MergeVars(vars, s.Flags)
	allowed := append([]string(nil), s.SpawnAllowlist...)
	sort.Strings(allowed)
	vars["allowed_agents"] = allowed
	return vars
}

func coordinatorSurfaceToolOverride(allowedAgents []string, resolvedTools []string) map[string][]string {
	if !containsAgentID(allowedAgents, coordinatorAgentID) {
		return nil
	}
	return map[string][]string{coordinatorAgentID: append([]string(nil), resolvedTools...)}
}

func containsAgentID(ids []string, want string) bool {
	for _, id := range ids {
		if strings.TrimSpace(id) == want {
			return true
		}
	}
	return false
}

// Hide external workers when web search is disabled.
func filterExternalSourceAgentViews(agents []prompts.SpawnAgentView) []prompts.SpawnAgentView {
	if len(agents) == 0 {
		return agents
	}
	out := make([]prompts.SpawnAgentView, 0, len(agents))
	for _, a := range agents {
		if agentdef.DeclaresAny(a.ID, agentdef.CapabilityExternal) {
			continue
		}
		out = append(out, a)
	}
	return out
}
