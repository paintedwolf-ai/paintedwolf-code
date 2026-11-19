package policyfacts

import (
	"github.com/lycaon/lycaon/internal/toolreach"
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
	for _, name := range []string{"ask_user", "secret_generate", "terminal_read", "command_stop", "view_image"} {
		for _, offered := range []bool{false, true} {
			names := []string{}
			if offered {
				names = append(names, name)
			}
			ctx := tools.WithCallReach(t.Context(), toolreach.Offered(names))
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
			{Operation: "file-read-data", Target: "/Users/person/Library/Keychains/login.keychain-db", Layer: confine.FloorReadCredentialStore, Recovery: confine.RecoverCredentialStore, Grant: "/Users/person/Library/Keychains/login.keychain-db"},
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
		{"read paths", gc.Refusals.RefusedReadPaths, []string{"/credentials/token"}},
		{"write paths", gc.Refusals.RefusedWritePaths, []string{"/workspace/output.txt"}},
		{"terminal reads", gc.Refusals.RefusedTerminalReadPaths, []string{"/state/store.db"}},
		{"terminal writes", gc.Refusals.RefusedTerminalWritePaths, []string{"/state/approvals.yaml"}},
		{"read grants", gc.Refusals.RefusedReadGrants, []string{"/credentials/token"}},
		{"credential store paths", gc.Refusals.RefusedCredentialStorePaths, []string{"/Users/person/Library/Keychains/login.keychain-db"}},
		{"write grants", gc.Refusals.RefusedWriteGrants, []string{"/workspace"}},
	} {
		if !slices.Equal(tc.got, tc.want) {
			t.Errorf("%s=%v, want %v", tc.name, tc.got, tc.want)
		}
	}
}

func TestObserveConfineDropsAncestorMetadataReadGrant(t *testing.T) {
	gc := oar.NewGuardContext()
	ObserveConfine(gc, confine.Observation{Applied: true, Refusals: confine.SandboxRefusals{
		Witness: confine.WitnessKernel,
		Refusals: []confine.SandboxRefusal{
			{Operation: "file-read-metadata", Target: "/Users/person/Library/Keychains", Layer: confine.FloorReadKeyMaterial, Recovery: confine.RecoverReadPath, Grant: "/Users/person/Library/Keychains"},
			{Operation: "file-read-metadata", Target: "/Users/person/Library/Keychains/login.keychain-db", Layer: confine.FloorReadKeyMaterial, Recovery: confine.RecoverReadPath, Grant: "/Users/person/Library/Keychains/login.keychain-db"},
		},
	}})
	wantPaths := []string{"/Users/person/Library/Keychains", "/Users/person/Library/Keychains/login.keychain-db"}
	if !slices.Equal(gc.Refusals.RefusedReadPaths, wantPaths) {
		t.Fatalf("RefusedReadPaths = %v, want %v", gc.Refusals.RefusedReadPaths, wantPaths)
	}
	wantGrants := []string{"/Users/person/Library/Keychains/login.keychain-db"}
	if !slices.Equal(gc.Refusals.RefusedReadGrants, wantGrants) {
		t.Fatalf("RefusedReadGrants = %v, want %v", gc.Refusals.RefusedReadGrants, wantGrants)
	}
}

func TestSharedTempRefusalsKeepEveryOtherCapability(t *testing.T) {
	items := []confine.SandboxRefusal{
		{Operation: "file-read-data", Target: "/key", Layer: confine.FloorReadKeyMaterial, Recovery: confine.RecoverReadPath, Grant: "/key"},
		{Operation: "file-write-data", Target: "/work/file", Layer: confine.FloorOutsideWriteRoots, Recovery: confine.RecoverWriteRoot, Grant: "/work"},
		{Operation: "file-write-create", Target: "/tmp/slot", Layer: confine.FloorSharedTemp, Recovery: confine.RecoverWriteRoot, Grant: "/tmp"},
		{Operation: "file-write-create", Target: "/state/file", Layer: confine.FloorControlPlane, Recovery: confine.RecoverNone},
		{Operation: "file-read-data", Target: "/credential", Layer: confine.FloorReadCredentialStore, Recovery: confine.RecoverCredentialStore},
		{Operation: "network-outbound", Target: "/socket", Recovery: confine.RecoverSocketPath, Grant: "/socket"},
		{Operation: "signal", Target: "pid", Recovery: confine.RecoverProcessControl},
		{Operation: "mount", Target: "device", Recovery: confine.RecoverHostExecution},
	}
	for mask := 1; mask < 1<<len(items); mask++ {
		var subset confine.SandboxRefusals
		for i, item := range items {
			if mask&(1<<i) != 0 {
				subset.Refusals = append(subset.Refusals, item)
			}
		}
		gc := oar.NewGuardContext()
		ObserveConfine(gc, confine.Observation{Applied: true, Refusals: subset})
		groups := [][]string{gc.Refusals.RefusedReadGrants, gc.Refusals.RefusedWriteGrants, nil, gc.Refusals.RefusedTerminalWritePaths, gc.Refusals.RefusedCredentialStorePaths, gc.Refusals.RefusedSocketPaths, gc.Refusals.RefusedSignals, gc.Refusals.UnsandboxedRefusals}
		for i, group := range groups {
			if i != 2 && (len(group) > 0) != (mask&(1<<i) != 0) {
				t.Fatalf("subset %b group %d=%v", mask, i, group)
			}
		}
		if slices.Contains(gc.Refusals.RefusedWritePaths, "/tmp/slot") || slices.Contains(gc.Refusals.RefusedWriteGrants, "/tmp") {
			t.Fatalf("temp leaked into ordinary writes in subset %b", mask)
		}
	}
}
