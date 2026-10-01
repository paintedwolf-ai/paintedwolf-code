package inject

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/capability"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
)

// ImplementSpawnInjectSentinel marks rendered spawn guidance.
const ImplementSpawnInjectSentinel = "<!-- lycaon-implement-spawn:v1 -->"

// RenderImplementSpawnInject renders the implement spawn guidance.
func RenderImplementSpawnInject(
	ctx context.Context,
	renderer *prompts.InjectRenderer,
	sessionID string,
	allowedAgents []string,
	maxInFlight int,
	surfaceID string,
	rootCount int,
	repoKnownEmpty bool,
	webSearchEnabled bool,
	catalog *extpacks.EffectiveCatalog,
	toolProfiles []sandbox.ToolProfile,
) (string, error) {
	if renderer == nil {
		return "", fmt.Errorf("inject renderer not configured")
	}
	surf, err := capability.Resolve(capability.ResolveInput{
		Profile:          surface.TurnProfile{SurfaceID: strings.TrimSpace(surfaceID)},
		RootCount:        rootCount,
		RepoKnownEmpty:   repoKnownEmpty,
		SpawnAllowlist:   allowedAgents,
		MaxInFlight:      maxInFlight,
		WebSearchEnabled: &webSearchEnabled,
		Catalog:          catalog,
		ToolProfiles:     toolProfiles,
	})
	if err != nil {
		return "", fmt.Errorf("resolve capability surface: %w", err)
	}
	vars := surf.MergeSpawnInjectVars()
	if err := prompts.MergeCoordinatorKickPolicyVars(vars); err != nil {
		return "", fmt.Errorf("spawn policy vars: %w", err)
	}
	block, err := anchor.RenderInform(ctx, anchor.InjectImplementSpawn, anchor.MatchContext{Surface: "coordinator", SessionID: sessionID}, renderer, vars)
	if err != nil {
		return "", err
	}
	block = strings.TrimSpace(block)
	if block == "" {
		return "", nil
	}
	if !strings.Contains(block, ImplementSpawnInjectSentinel) {
		return "", fmt.Errorf("implement-spawn inject missing sentinel %q", ImplementSpawnInjectSentinel)
	}
	return block, nil
}

// ImplementSpawnInjectFingerprint hashes spawn roster inputs.
func ImplementSpawnInjectFingerprint(allowedAgents []string, maxInFlight int, surfaceID string, rootCount int, repoKnownEmpty bool, webSearchEnabled bool, catalog *extpacks.EffectiveCatalog, toolProfiles []sandbox.ToolProfile) string {
	surf, err := capability.Resolve(capability.ResolveInput{
		Profile:          surface.TurnProfile{SurfaceID: strings.TrimSpace(surfaceID)},
		RootCount:        rootCount,
		RepoKnownEmpty:   repoKnownEmpty,
		SpawnAllowlist:   allowedAgents,
		MaxInFlight:      maxInFlight,
		WebSearchEnabled: &webSearchEnabled,
		Catalog:          catalog,
		ToolProfiles:     toolProfiles,
	})
	if err != nil {
		allowed := append([]string(nil), allowedAgents...)
		sort.Strings(allowed)
		return prompts.SpawnRosterFingerprint(prompts.SpawnRosterData{
			MaxInFlight: maxInFlight,
			SpawnAgents: []prompts.SpawnAgentView{{ID: strings.Join(allowed, ",")}},
		}, nil)
	}
	return prompts.SpawnRosterFingerprint(surf.Roster, surf.ExcludedDisclosures)
}
