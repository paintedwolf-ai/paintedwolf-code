package contract

import (
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/capability"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/webresearch"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

const webResearcherAgent = "web-researcher"

// The web search switch is the master gate: the same surface carries the web
// tools and the researcher when it is on and none of them when it is off.
func TestCapabilitySurfaceOmitsWebToolsWhenSearchDisabled(t *testing.T) {
	t.Parallel()
	profiles, err := sandbox.LoadToolProfiles()
	contractcheck.FailErr(t, "LoadToolProfiles", err)
	if !slices.Contains(spawn.AmbientAllowedAgents(), webResearcherAgent) {
		t.Fatalf("ambient agents %v lack %s", spawn.AmbientAllowedAgents(), webResearcherAgent)
	}
	resolve := func(enabled bool) capability.CapabilitySurface {
		t.Helper()
		surf, err := capability.Resolve(capability.ResolveInput{
			Profile:          surface.TurnProfile{SurfaceID: spawn.SurfaceImplementRouting},
			ToolProfiles:     profiles,
			RootCount:        1,
			SpawnAllowlist:   inject.ResolveAgentRoster(spawn.SurfaceImplementRouting, spawn.AmbientAllowedAgents(), 1, false, enabled).Effective,
			WebSearchEnabled: &enabled,
		})
		contractcheck.FailErr(t, "resolve capability surface", err)
		return surf
	}

	on := resolve(true)
	if !slices.Contains(on.SpawnAllowlist, webResearcherAgent) || !on.Flags.CanSpawnWebResearch {
		t.Fatalf("search enabled: spawn allowlist %v, can spawn web research %t; want the researcher", on.SpawnAllowlist, on.Flags.CanSpawnWebResearch)
	}

	off := resolve(false)
	for _, tool := range []string{webresearch.SearchToolName, webresearch.FetchURLToolName} {
		if slices.Contains(off.ResolvedTools, tool) {
			t.Errorf("search disabled: resolved tools still contain %s: %v", tool, off.ResolvedTools)
		}
	}
	if slices.Contains(off.SpawnAllowlist, webResearcherAgent) || off.Flags.CanSpawnWebResearch {
		t.Errorf("search disabled: spawn allowlist %v, can spawn web research %t; want no researcher", off.SpawnAllowlist, off.Flags.CanSpawnWebResearch)
	}
}
