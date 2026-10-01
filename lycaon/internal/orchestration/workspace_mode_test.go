package orchestration

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestValidateWorkspaceMode(t *testing.T) {
	if err := validateWorkspaceMode(WorkspaceShared); err != nil {
		testutil.FailErr(t, "validateWorkspaceMode failed", err)
	}
	if err := validateWorkspaceMode(WorkspaceIsolated); err != nil {
		testutil.FailErr(t, "validateWorkspaceMode failed", err)
	}
	if err := validateWorkspaceMode("invalid"); err == nil {
		t.Fatal("expected error for invalid mode")
	}
}

func TestEffectiveWorkspaceModeDefaultsShared(t *testing.T) {
	if got := effectiveWorkspaceMode(""); got != WorkspaceShared {
		t.Fatalf("mode = %q", got)
	}
}

func TestTopologyRequiresIsolation(t *testing.T) {
	if !TopologyRequiresIsolation(TopologySpec{Pattern: TopologyPack, WorkspaceMode: WorkspaceIsolated}) {
		t.Fatal("isolated pack should require isolation")
	}
	if TopologyRequiresIsolation(TopologySpec{Pattern: TopologyPack, WorkspaceMode: WorkspaceShared}) {
		t.Fatal("shared pack should not require isolation flag")
	}
}
