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
	for _, tc := range []struct {
		pattern TopologyPattern
		mode    WorkspaceMode
		want    bool
	}{
		{TopologyPack, WorkspaceIsolated, true},
		{TopologyPack, WorkspaceShared, false},
		{TopologyFanOut, WorkspaceIsolated, true},
		{TopologyFanOut, WorkspaceShared, false},
		{TopologyFanOut, "", false},
		{TopologyPipeline, WorkspaceIsolated, false},
	} {
		got := TopologyRequiresIsolation(TopologySpec{Pattern: tc.pattern, WorkspaceMode: tc.mode})
		if got != tc.want {
			t.Errorf("TopologyRequiresIsolation(%s, %q) = %v, want %v", tc.pattern, tc.mode, got, tc.want)
		}
	}
}
