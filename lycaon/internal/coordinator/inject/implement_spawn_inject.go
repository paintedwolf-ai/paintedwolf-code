package inject

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/capability"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/prompts"
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
	reader prompts.Reader,
	repoKnownEmpty bool,
) (string, error) {
	if renderer == nil {
		return "", fmt.Errorf("inject renderer not configured")
	}
	webSearchEnabled := reader.WebSearch()
	surf, err := capability.Resolve(capability.ResolveInput{
		Profile:          surface.TurnProfile{SurfaceID: strings.TrimSpace(reader.SurfaceID())},
		RootCount:        reader.RootCount(),
		RepoKnownEmpty:   repoKnownEmpty,
		SpawnAllowlist:   allowedAgents,
		MaxInFlight:      maxInFlight,
		WebSearchEnabled: &webSearchEnabled,
		Catalog:          reader.Catalog(),
		ToolProfiles:     reader.Profiles(),
	})
	if err != nil {
		return "", fmt.Errorf("resolve capability surface: %w", err)
	}
	vars := reader.Facts()
	for k, v := range surf.MergeSpawnInjectVars() {
		vars[k] = v
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
