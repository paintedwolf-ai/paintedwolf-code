package capability_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/capability"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/webresearch"
)

func TestResolveCapabilitySurfaceImplementRouting(t *testing.T) {
	surf, err := capability.Resolve(capability.ResolveInput{
		Profile:        surface.TurnProfile{SurfaceID: spawn.SurfaceImplementRouting},
		RootCount:      1,
		SpawnAllowlist: inject.ResolveAgentRoster(spawn.SurfaceImplementRouting, spawn.AmbientAllowedAgents(), 1, false, true).Effective,
		MaxInFlight:    spawn.MaxInFlightTaskWorkers,
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !surf.Flags.HasFileTools {
		t.Fatal("1-root routing should have file tools")
	}
	if len(surf.ResolvedTools) == 0 {
		t.Fatal("expected resolved tools")
	}
	if len(surf.SpawnAllowlist) == 0 {
		t.Fatal("expected spawn allowlist")
	}
	if len(surf.ExcludedDisclosures) == 0 {
		t.Fatal("expected excluded disclosures for disallowed catalog agents")
	}
}

func TestResolveCapabilitySurfaceNoFolder(t *testing.T) {
	surf, err := capability.Resolve(capability.ResolveInput{
		Profile:        surface.TurnProfile{SurfaceID: spawn.SurfaceImplementRouting},
		RootCount:      0,
		SpawnAllowlist: inject.ResolveAgentRoster(spawn.SurfaceImplementRouting, spawn.AmbientAllowedAgents(), 0, false, true).Effective,
		MaxInFlight:    spawn.MaxInFlightTaskWorkers,
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if surf.Flags.HasFileTools {
		t.Fatal("0-root surface should not grant file tools")
	}
	if len(surf.SpawnAllowlist) != 1 || surf.SpawnAllowlist[0] != "web-researcher" {
		t.Fatalf("spawn allowlist = %v", surf.SpawnAllowlist)
	}
}

func TestResolveCapabilitySurfaceSearchDisabled(t *testing.T) {
	disabled := false
	surf, err := capability.Resolve(capability.ResolveInput{
		Profile:          surface.TurnProfile{SurfaceID: spawn.SurfaceImplementRouting},
		RootCount:        0,
		SpawnAllowlist:   inject.ResolveAgentRoster(spawn.SurfaceImplementRouting, spawn.AmbientAllowedAgents(), 0, false, false).Effective,
		MaxInFlight:      spawn.MaxInFlightTaskWorkers,
		WebSearchEnabled: &disabled,
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if surf.Flags.CanSpawnWebResearch {
		t.Fatal("expected CanSpawnWebResearch false when search disabled")
	}
	if len(surf.SpawnAllowlist) != 0 {
		t.Fatalf("spawn allowlist = %v want []", surf.SpawnAllowlist)
	}
	for _, tool := range surf.ResolvedTools {
		if tool == webresearch.SearchToolName || tool == webresearch.FetchURLToolName {
			t.Fatalf("resolved tools still include web research tool %q: %v", tool, surf.ResolvedTools)
		}
	}
}
