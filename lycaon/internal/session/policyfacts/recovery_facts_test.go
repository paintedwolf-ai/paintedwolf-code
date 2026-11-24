package policyfacts

import (
	"reflect"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/toolreach"
	"github.com/lycaon/lycaon/internal/toolrejection"

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

// Every post-invoke refusal code carries the observed destination, so a guard
// keyed on any of them can name it.
func TestObserveConfinePublishesPostInvokeRefusalData(t *testing.T) {
	gc := oar.NewGuardContext()
	gc.Invocation.Tool = "command"
	ObserveConfine(gc, confine.Observation{Applied: true, Destination: "service.test:8443", Port: 8443})
	want := map[string]any{"tool": "command", "destination": "service.test:8443", "port": "8443"}
	for _, code := range []string{isolation.CodeBoundaryRefused, isolation.CodeRemotePackageDestinationDenied, toolrejection.VerifyUnverifiableCode} {
		if got := gc.RejectData[code]; !reflect.DeepEqual(got, want) {
			t.Errorf("reject data for %s = %v want %v", code, got, want)
		}
	}
	unapplied := oar.NewGuardContext()
	ObserveConfine(unapplied, confine.Observation{Destination: "service.test"})
	if len(unapplied.RejectData) != 0 {
		t.Fatalf("unapplied boundary published reject data: %v", unapplied.RejectData)
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
		{Operation: "network-bind", Target: "local:127.0.0.1:5301", Recovery: confine.RecoverLocalListen},
		{Operation: "network-outbound", Target: "remote:127.0.0.1:5302", Recovery: confine.RecoverOutboundPort},
		{Operation: "file-read-data", Target: "/state/store", Layer: confine.FloorReadControlPlane, Recovery: confine.RecoverNone},
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
		groups := [][]string{gc.Refusals.RefusedReadGrants, gc.Refusals.OrdinaryWriteGrants, subset.SharedTempRoots(), gc.Refusals.RefusedTerminalWritePaths, gc.Refusals.RefusedCredentialStorePaths, gc.Refusals.RefusedSocketPaths, gc.Refusals.RefusedSignals, gc.Refusals.UnsandboxedRefusals, gc.Refusals.RefusedListenPorts, gc.Refusals.RefusedConnectPorts, gc.Refusals.RefusedTerminalReadPaths}
		for i, group := range groups {
			if (len(group) > 0) != (mask&(1<<i) != 0) {
				t.Fatalf("subset %b group %d=%v", mask, i, group)
			}
		}
		hasTemp := mask&(1<<2) != 0
		if slices.Contains(gc.Refusals.RefusedWritePaths, "/tmp/slot") != hasTemp || slices.Contains(gc.Refusals.RefusedWriteGrants, "/tmp") != hasTemp {
			t.Fatalf("published write observation missing from subset %b", mask)
		}
		if slices.Contains(gc.Refusals.OrdinaryWritePaths, "/tmp/slot") || slices.Contains(gc.Refusals.OrdinaryWriteGrants, "/tmp") {
			t.Fatalf("temp leaked into ordinary writes in subset %b", mask)
		}
	}
}

func TestObserveConfinePublishesSSHGitTransportAndGHCredentialStoreFacts(t *testing.T) {
	gc := oar.NewGuardContext()
	gc.Invocation.Tool = "command"
	ObserveConfine(gc, confine.Observation{
		Applied:         true,
		FailedStages:    []string{"GH_HOST=github.com external -- /usr/local/bin/gh api user"},
		SucceededStages: []string{"git status"},
		Refusals: confine.SandboxRefusals{
			Refusals: []confine.SandboxRefusal{
				{Operation: "file-read-data", Target: "/Users/person/Library/Keychains/login.keychain-db", Layer: confine.FloorReadCredentialStore, Recovery: confine.RecoverCredentialStore},
			},
		},
		GitTransports: []confine.GitTransportObservation{
			{ExitCode: 0, Cause: confine.GitTransportCauseEstablished},
			{
				ExitCode:     128,
				Command:      "git push",
				Cause:        confine.GitTransportCauseEndpointDenied,
				Stage:        "policy",
				Destination:  "ssh://127.0.0.1:2222/repo.git",
				Host:         "127.0.0.1",
				Port:         2222,
				PolicyReason: "loopback_port_ungranted",
				ErrorClass:   "denied",
			},
		},
		SSHRecoveries: []confine.SSHRecoveryObservation{
			{Cause: ""},
			{
				Cause:       confine.SSHFailureHostKeyChanged,
				Stage:       "git fetch origin",
				Client:      "git",
				Destination: "github.com:22",
				TrustPath:   "/Users/person/.ssh/known_hosts",
				ProbedPaths: []string{"/Users/person/.ssh/id_ed25519"},
			},
		},
	})
	if !gc.Execution.GHCredentialStoreRefused {
		t.Fatal("expected GHCredentialStoreRefused for failed gh stage with credential store refusal")
	}
	if !gc.Execution.GitTransportLoopbackRetryable || gc.Execution.GitTransportPort != "2222" {
		t.Fatalf("git transport facts = %+v", gc.Execution)
	}
	if gc.Execution.SSHFailureCause != string(confine.SSHFailureHostKeyChanged) || !gc.Execution.SSHClientIsNativeGit || gc.Execution.SSHTrustPath != "/Users/person/.ssh/known_hosts" {
		t.Fatalf("ssh recovery facts = %+v", gc.Execution)
	}
	data := gc.RejectData[isolation.CodeSSHHostKeyChanged]
	if data["gh_credential_store_refused"] != true || data["ssh_failure_cause"] != string(confine.SSHFailureHostKeyChanged) || data["git_transport_port"] != "2222" {
		t.Fatalf("reject data = %+v", data)
	}
	if hasFailedExecutableStage([]string{"FOO=1 external -- /usr/bin/curl https://example.com"}, "gh") {
		t.Fatal("non-gh stage matched gh")
	}
}
