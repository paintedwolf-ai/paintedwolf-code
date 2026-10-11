package policyfacts

import (
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSessionOccurrencesPreserveOfferedRecoverySnapshot(t *testing.T) {
	manager := New(nil)
	for _, name := range []string{"ask_user", "secret_generate", "terminal_read", "command_stop"} {
		for _, offered := range []bool{false, true} {
			names := []string{}
			if offered {
				names = append(names, name)
			}
			ctx := tools.WithRecoveryTools(t.Context(), names)
			gc := oar.NewGuardContext()
			manager.FillSessionFacts(ctx, gc, &api.Session{ID: "fixture"}, "read", nil)
			if len(gc.ObservationData) != 0 {
				t.Fatal("recovery observations assembled before reference")
			}
			testutil.FailErr(t, "publish recovery fact", gc.Ensure("paintedwolf.can_"+name))
			if gc.ObservationData["can_"+name] != offered {
				t.Fatalf("recovery %s did not preserve offered=%v", name, offered)
			}
		}
	}
}

func TestMixedRefusalsSeparateTerminalPathsFromRecoveryGrants(t *testing.T) {
	gc := oar.NewGuardContext()
	ObserveConfine(gc, confine.Observation{Applied: true, Refusals: confine.SandboxRefusals{
		Witness: confine.WitnessIncomplete,
		Refusals: []confine.SandboxRefusal{
			{Operation: "file-read-data", Target: "/state/store.db", Layer: confine.FloorReadControlPlane, Recovery: confine.RecoverNone},
			{Operation: "file-read-data", Target: "/credentials/token", Layer: confine.FloorReadKeyMaterial, Recovery: confine.RecoverReadPath, Grant: "/credentials/token"},
			{Operation: "file-write-data", Target: "/state/approvals.yaml", Layer: confine.FloorControlPlane, Recovery: confine.RecoverNone},
			{Operation: "file-write-data", Target: "/workspace/output.txt", Layer: confine.FloorOutsideWriteRoots, Recovery: confine.RecoverWriteRoot, Grant: "/workspace"},
			{Operation: "file-read-metadata", Target: "/state/store.db", Layer: confine.FloorReadControlPlane, Recovery: confine.RecoverNone},
			{Operation: "file-write-create", Target: "/state/approvals.yaml", Layer: confine.FloorControlPlane, Recovery: confine.RecoverNone},
		},
	}})
	for _, tc := range []struct {
		name      string
		got, want []string
	}{
		{"read paths", gc.Refusals.RefusedReadPaths, []string{"/state/store.db", "/credentials/token"}},
		{"write paths", gc.Refusals.RefusedWritePaths, []string{"/state/approvals.yaml", "/workspace/output.txt"}},
		{"terminal reads", gc.Refusals.RefusedTerminalReadPaths, []string{"/state/store.db"}},
		{"terminal writes", gc.Refusals.RefusedTerminalWritePaths, []string{"/state/approvals.yaml"}},
		{"read grants", gc.Refusals.RefusedReadGrants, []string{"/credentials/token"}},
		{"write grants", gc.Refusals.RefusedWriteGrants, []string{"/workspace"}},
	} {
		if !slices.Equal(tc.got, tc.want) {
			t.Errorf("%s=%v, want %v", tc.name, tc.got, tc.want)
		}
	}
	if gc.Refusals.SandboxRefusalWitness != string(confine.WitnessIncomplete) {
		t.Fatal("observed reporting gap was discarded")
	}
	if len(gc.Execution.SandboxRefusals) != 6 {
		t.Fatal("observed refusal reports were discarded during path grouping")
	}
}
