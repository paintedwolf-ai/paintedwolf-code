package capability_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/capability"
)

func TestDeriveZeroRoot(t *testing.T) {
	caps := capability.Derive(0,
		[]string{"task", "web_search", "fetch_url", "wait"},
		[]string{"web-researcher"},
	)
	if caps.HasWorkspaceRoots || caps.HasFileTools || caps.CanOrient || caps.CanSpawnImplementer {
		t.Fatalf("0-root caps = %+v want web-only", caps)
	}
	if !caps.CanSpawnWebResearch {
		t.Fatal("expected can_spawn_web_research at 0 roots")
	}
	if !caps.CanSpawnWorkers {
		t.Fatal("expected can_spawn_workers at 0 roots with web-researcher")
	}
}

func TestDeriveFolderMode(t *testing.T) {
	caps := capability.Derive(1,
		[]string{"read", "grep", "pack_board", "task"},
		[]string{"implementer", "web-researcher"},
	)
	if !caps.HasWorkspaceRoots || !caps.HasFileTools || !caps.CanOrient || !caps.CanSpawnImplementer || !caps.CanSpawnWebResearch || !caps.CanSpawnWorkers {
		t.Fatalf("folder caps = %+v", caps)
	}
}

func TestDeriveDispatchSurfaceSplitsWorkspaceRootsFromFileTools(t *testing.T) {
	caps := capability.Derive(1,
		[]string{"promote_work", "discard_work", "task", "wait"},
		[]string{"implementer"},
	)
	if !caps.HasWorkspaceRoots {
		t.Fatalf("expected HasWorkspaceRoots at 1 root, got %+v", caps)
	}
	if caps.HasFileTools {
		t.Fatalf("dispatch surface without file/shell/survey/source_ledger tools set HasFileTools: %+v", caps)
	}
}

func TestDeriveNoWorkers(t *testing.T) {
	caps := capability.Derive(0,
		[]string{"read"},
		nil,
	)
	if caps.HasWorkspaceRoots || caps.HasFileTools || caps.CanSpawnWorkers {
		t.Fatalf("expected 0-root no-worker caps, got %+v", caps)
	}
}
