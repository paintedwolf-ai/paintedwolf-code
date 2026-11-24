package confine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestBuildKnownHostsMirrorWritesCollectedMaterial(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(configdir.EnvConfigDir, filepath.Join(t.TempDir(), "config"))

	entry := "github.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl"
	sshDir := filepath.Join(home, ".ssh")
	testutil.FailErr(t, "create .ssh", os.MkdirAll(sshDir, 0o700))
	testutil.FailErr(t, "seed known_hosts",
		os.WriteFile(filepath.Join(sshDir, "known_hosts"), []byte(entry+"\n"), 0o600))

	path := buildKnownHostsMirror()
	if path == "" {
		t.Fatal("mirror path is empty: host key material was collected but never committed")
	}
	data, err := os.ReadFile(path) // #nosec G304 -- path minted by the mirror under a test config dir
	testutil.FailErr(t, "read mirror", err)
	if !strings.Contains(string(data), entry) {
		t.Fatalf("mirror is missing the collected entry: %q", data)
	}
	info, err := os.Stat(path)
	testutil.FailErr(t, "stat mirror", err)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mirror mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestMediatedProjectUsesExactHostKeyFile(t *testing.T) {
	t.Setenv(configdir.EnvConfigDir, t.TempDir())
	c := Confinement{Network: NetworkProxyOnly, ProjectID: "project-1"}
	path, err := PrepareSSHKnownHosts(c)
	testutil.FailErr(t, "prepare project keys", err)
	testutil.FailErr(t, "save key", os.WriteFile(path, []byte("fixture"), 0600))
	again, err := PrepareSSHKnownHosts(c)
	testutil.FailErr(t, "reopen project keys", err)
	data, err := os.ReadFile(again)
	testutil.FailErr(t, "read project key", err)
	if again != path || string(data) != "fixture" {
		t.Fatalf("key file %q contains %q", again, data)
	}
	for _, access := range []FilesystemAccess{AccessRead, AccessWrite} {
		fs, err := filesystemRules(c)
		testutil.FailErr(t, "compile project rules", err)
		exact, sibling := fs.Verdict(access, path), fs.Verdict(access, path+"2")
		if !exact.Allowed || sibling.Allowed {
			t.Errorf("%s project key verdict %+v, sibling verdict %+v", access, exact, sibling)
		}
	}
	c.ProjectID = "project-2"
	fs, err := filesystemRules(c)
	testutil.FailErr(t, "compile other project rules", err)
	if fs.Verdict(AccessWrite, path).Allowed || fs.Verdict(AccessRead, path).Allowed {
		t.Fatal("another project can access the key file")
	}
	mirror := enginepaths.SSHKnownHostsMirrorUnder(os.Getenv(configdir.EnvConfigDir))
	if !fs.Verdict(AccessRead, mirror).Allowed || fs.Verdict(AccessRead, mirror+"2").Allowed || fs.Verdict(AccessWrite, mirror).Allowed {
		t.Fatal("host mirror must allow only exact-file reads")
	}
	c.Network = NetworkDeny
	path, err = PrepareSSHKnownHosts(c)
	if path != "" || err != nil {
		t.Fatalf("offline key file = %q, %v", path, err)
	}
}

func TestSummarizeGitTransportAndMergeObservations(t *testing.T) {
	setup := SummarizeGitTransport("exec-1", "git ls-remote origin", 128, true, "", "service_start_failed", "github.com:22", "github.com", 22, nil)
	if setup.Cause != GitTransportCauseSetupFailed || !setup.Cause.PreAuthFailure() || setup.Stage != "host_setup" || setup.PolicyReason != "service_start_failed" {
		t.Fatalf("setup summary = %+v", setup)
	}

	attempts := []GitTransportAttempt{
		{Ordinal: 1, Destination: "127.0.0.1:2201", Host: "127.0.0.1", Port: 2201, Stage: "dial", Outcome: "established", ReplyCode: 0},
		{Ordinal: 2, Destination: "127.0.0.1:2202", Host: "127.0.0.1", Port: 2202, Stage: "policy", Outcome: "endpoint_denied", ReplyCode: 2, PolicyReason: "loopback_port_ungranted"},
	}
	partial := SummarizeGitTransport("exec-2", "git fetch --all", 1, true, "", "", "127.0.0.1:2201", "127.0.0.1", 2201, attempts)
	if partial.Cause != GitTransportCauseEndpointDenied || !partial.Cause.PreAuthFailure() || partial.Destination != "127.0.0.1:2202" || len(partial.Attempts) != 2 {
		t.Fatalf("partial summary = %+v", partial)
	}

	okSummary := SummarizeGitTransport("exec-3", "git fetch origin", 128, true, "", "", "127.0.0.1:2201", "127.0.0.1", 2201, attempts[:1])
	if okSummary.Cause != GitTransportCauseEstablished || okSummary.Cause.PreAuthFailure() {
		t.Fatalf("established summary = %+v", okSummary)
	}

	unknown := SummarizeGitTransport("exec-4", "git ls-remote origin", 128, true, "", "", "127.0.0.1:2201", "127.0.0.1", 2201, nil)
	if unknown.Cause != GitTransportCauseUnknown || unknown.Cause.PreAuthFailure() {
		t.Fatalf("unknown summary = %+v", unknown)
	}

	none := SummarizeGitTransport("exec-5", "git ls-remote ./local", 0, false, "", "", "", "", 0, nil)
	if none.Cause != GitTransportCauseNone {
		t.Fatalf("none summary = %+v", none)
	}

	obs1 := Observation{Applied: true, Network: "proxy_only", GitTransports: []GitTransportObservation{setup}}
	obs2 := Observation{Applied: true, Network: "proxy_only", GitTransports: []GitTransportObservation{partial}}
	merged := obs1.Merge(obs2)
	if len(merged.GitTransports) != 2 || merged.GitTransports[0].ExecutionID != "exec-1" || merged.GitTransports[1].ExecutionID != "exec-2" {
		t.Fatalf("merged GitTransports = %+v", merged.GitTransports)
	}
}

func TestStampRefusalIgnoresInheritedProbesAndRetainsGitTransports(t *testing.T) {
	boundary := Boundary{Applied: true, Network: NetworkProxyOnly}
	transport := GitTransportObservation{
		ExecutionID: "exec-ok",
		Command:     "git fetch origin",
		ExitCode:    0,
		Cause:       GitTransportCauseEstablished,
		Destination: "github.com:22",
	}
	stamped := StampRefusal("command", "sess-1", boundary, RefusalContext{
		MediatedNetwork: []EgressHost{
			{Host: "probe.example", Port: 443, Allowed: false, Inherited: true, Attempts: 1},
			{Host: "github.com", Port: 22, Allowed: true, Attempts: 1},
		},
		GitTransports: []GitTransportObservation{transport},
	})
	if stamped.Attribution != "" || len(stamped.GuidanceCodes) != 0 || stamped.Observation.HasSignal(SignalBoundaryRefused) {
		t.Fatalf("inherited background probe created refusal on successful command: %+v", stamped)
	}
	if len(stamped.Observation.GitTransports) != 1 || stamped.Observation.GitTransports[0].ExecutionID != "exec-ok" {
		t.Fatalf("GitTransports lost during StampRefusal: %+v", stamped.Observation.GitTransports)
	}
}
