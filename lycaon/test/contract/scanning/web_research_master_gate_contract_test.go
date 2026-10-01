package contract

import (
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/capability"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/webresearch"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestCapabilitySurfaceOmitsWebToolsWhenSearchDisabled(t *testing.T) {
	t.Parallel()
	off := false
	surf, err := capability.Resolve(capability.ResolveInput{
		Profile:          surface.TurnProfile{SurfaceID: spawn.SurfaceImplementRouting},
		RootCount:        1,
		SpawnAllowlist:   inject.ResolveAgentRoster(spawn.SurfaceImplementRouting, spawn.AmbientAllowedAgents(), 1, false, false).Effective,
		WebSearchEnabled: &off,
	})
	contractcheck.FailErr(t, "Resolve disabled", err)
	if slices.Contains(surf.ResolvedTools, webresearch.SearchToolName) {
		t.Fatalf("resolved tools still contain web_search: %v", surf.ResolvedTools)
	}
	if slices.Contains(surf.ResolvedTools, webresearch.FetchURLToolName) {
		t.Fatalf("resolved tools still contain fetch_url: %v", surf.ResolvedTools)
	}
	if slices.Contains(surf.SpawnAllowlist, "web-researcher") {
		t.Fatalf("spawn allowlist still contains web-researcher: %v", surf.SpawnAllowlist)
	}
	if surf.Flags.CanSpawnWebResearch {
		t.Fatal("CanSpawnWebResearch must be false when search disabled")
	}

	on := true
	surfOn, err := capability.Resolve(capability.ResolveInput{
		Profile:          surface.TurnProfile{SurfaceID: spawn.SurfaceImplementRouting},
		RootCount:        1,
		SpawnAllowlist:   inject.ResolveAgentRoster(spawn.SurfaceImplementRouting, spawn.AmbientAllowedAgents(), 1, false, true).Effective,
		WebSearchEnabled: &on,
	})
	contractcheck.FailErr(t, "Resolve enabled", err)
	if !slices.Contains(surfOn.ResolvedTools, webresearch.SearchToolName) &&
		!slices.Contains(surfOn.ResolvedTools, webresearch.FetchURLToolName) {
		// Surface may omit tools for other policy reasons; still require researcher
		// spawn restoration when the default agent list includes it.
		if !slices.Contains(surfOn.SpawnAllowlist, "web-researcher") &&
			slices.Contains(spawn.AmbientAllowedAgents(), "web-researcher") {
			t.Fatalf("enabled search should restore web-researcher when in session agents; got %v", surfOn.SpawnAllowlist)
		}
	}
}
