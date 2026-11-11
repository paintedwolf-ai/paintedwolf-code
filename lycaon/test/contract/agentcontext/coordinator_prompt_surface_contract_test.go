package contract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/surfacecatalog"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/toolcontract"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestCoordinatorSurfaceInvariants_specialistAgentsNotOnWire(t *testing.T) {
	t.Parallel()
	surfaces := loadImplementSurfaces(t)
	wire := append(append(append([]string(nil), surfaces[surfaceImplementRouting]...), surfaces[surfaceImplementDispatch]...), surfaces[surfaceImplementSynthesis]...)
	for _, agentID := range specialistSpawnAgents {
		if contractcheck.ContainsString(wire, agentID) {
			t.Fatalf("agent %q must be spawned via task(), not listed as coordinator wire tool", agentID)
		}
	}
}

func TestCoordinatorSurfaceInvariants_specialistAgentsSpawnable(t *testing.T) {
	t.Parallel()
	allowed := spawn.AmbientAllowedAgents()
	for _, agentID := range specialistSpawnAgents {
		if !contractcheck.ContainsString(allowed, agentID) {
			t.Fatalf("specialist agent %q must be in AmbientAllowedAgents", agentID)
		}
	}
}

func TestCoordinatorSurfaceInvariants_workersExpandCapabilityBeyondCoordinatorWire(t *testing.T) {
	t.Parallel()
	coord := loadCoordinatorProfileTools(t)
	surfaces := loadImplementSurfaces(t)
	synthesis := surfaces[surfaceImplementSynthesis]

	profiles, err := sandbox.LoadToolProfiles()
	contractcheck.FailErr(t, "LoadToolProfiles", err)
	byID := map[string]sandbox.ToolProfile{}
	for _, p := range profiles {
		byID[p.ID] = p
	}
	impl, ok := byID["implement"]
	if !ok {
		t.Fatal("missing implement profile")
	}
	explore, ok := byID["explore_readonly"]
	if !ok {
		t.Fatal("missing explore_readonly profile")
	}
	mutation, err := toolcontract.NewCommandEffects(toolcontract.CommandMutation)
	contractcheck.FailErr(t, "declare worker mutation effect", err)
	if !impl.Tools["command"] || !impl.CommandEffects.Allows(mutation) {
		t.Fatal("implementer profile must grant command mutation capability")
	}
	catalog, err := surfacecatalog.Load()
	contractcheck.FailErr(t, "load synthesis effect boundary", err)
	row, err := catalog.Surface(surfaceImplementSynthesis)
	contractcheck.FailErr(t, "load synthesis declaration", err)
	effects, err := toolcontract.NewCommandEffects(row.CommandEffects...)
	contractcheck.FailErr(t, "compile synthesis effects", err)
	if effects.Allows(mutation) {
		t.Fatal("synthesis command must not admit worker mutation effects")
	}
	if contractcheck.ContainsString(synthesis, "write") {
		t.Fatal("implement_synthesis wire must not expose write — delegate to implementer via task()")
	}
	readOnly, _ := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly)
	if !explore.Tools["command"] || !explore.ToolSticky("command") || explore.CommandEffects != readOnly {
		t.Fatalf("explore_readonly profile must grant eager read_only command; got allowed=%v sticky=%v effects=%v", explore.Tools["command"], explore.ToolSticky("command"), explore.CommandEffects.Classes())
	}
	if explore.Tools["write"] {
		t.Fatal("explore_readonly profile must not grant write")
	}
	if explore.Tools["git_diff"] || impl.Tools["write"] {
		t.Fatal("retired RPC operations must not return as public profile tools")
	}
	// Coordinator grants command; the turn surface gates it.
	if !coord.Tools["command"] {
		t.Fatal("coordinator profile must grant command as its ceiling (surface-gated per turn)")
	}
	if contractcheck.ContainsString(surfaces[surfaceImplementDispatch], "command") {
		t.Fatal("implement_dispatch must not expose command — orchestration delegates execution to workers")
	}
}
