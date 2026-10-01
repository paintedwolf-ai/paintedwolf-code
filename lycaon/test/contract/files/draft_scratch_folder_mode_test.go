package contract

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/spawn"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Draft scratch workspaces count as folder mode — full coordinator surface and
// implementer spawn, not the no-folder web-research-only profile.
func TestDraftScratchUnlocksFolderModeCapabilities(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	ctx := context.Background()
	reg := project.NewMemoryRegistry()
	p, err := reg.Create(ctx, project.CreateParams{Draft: true})
	contractcheck.FailErr(t, "create draft project", err)
	if len(p.Roots) != 1 {
		t.Fatalf("len(p.Roots) = %d want 1", len(p.Roots))
	}

	profile := surface.TurnProfile{SurfaceID: "implement_investigate"}
	folderPlan, err := surface.CompileToolPlan(profile, 1)
	contractcheck.FailErr(t, "CompileToolPlan folder", err)
	noFolderPlan, err := surface.CompileToolPlan(profile, 0)
	contractcheck.FailErr(t, "CompileToolPlan no-folder", err)
	folderTools := folderPlan.ImmediateNames()
	noFolderTools := noFolderPlan.ImmediateNames()
	if len(folderTools) <= len(noFolderTools) {
		t.Fatalf("folder tool count %d must exceed no-folder %d", len(folderTools), len(noFolderTools))
	}
	for _, name := range []string{"read", "grep"} {
		if !toolInList(folderTools, name) {
			t.Fatalf("folder mode missing tool %q", name)
		}
	}
	if !folderPlan.Addressable("pack_board") {
		t.Fatalf("folder mode cannot load tool %q", "pack_board")
	}

	spawnAgents := inject.ResolveAgentRoster(spawn.SurfaceImplementDispatch,
		spawn.AmbientAllowedAgents(), len(p.Roots), false, true).Effective
	if !toolInList(spawnAgents, "implementer") {
		t.Fatalf("spawn roster %v missing implementer", spawnAgents)
	}
	if toolInList(spawnAgents, "web-researcher") && len(spawnAgents) == 1 {
		t.Fatalf("draft scratch must not be web-research-only spawn roster")
	}
}

func toolInList(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
