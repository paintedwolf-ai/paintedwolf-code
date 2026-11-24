package gitcmd

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/lineage"
	"github.com/lycaon/lycaon/internal/testutil"
)

func isolateWithoutHostAgent(t *testing.T) context.Context {
	t.Helper()
	t.Setenv("SSH_AUTH_SOCK", "")
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path="+filepath.Join(t.TempDir(), "no-dbus.sock"))
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	return ctx
}

func TestEvaluateMediatedGitSSHHostKeysAndMissingAgent(t *testing.T) {
	if obs, refusals, suppress := evaluateGitSSHRecovery(t.Context(), gitSSHLaunchInput{failed: false}); obs != nil || refusals != nil || suppress {
		t.Fatalf("succeeded launch produced obs=%+v refusals=%+v suppress=%v", obs, refusals, suppress)
	}

	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	noAgentCtx := isolateWithoutHostAgent(t)

	c := confine.Confinement{Network: confine.NetworkProxyOnly, ProjectID: "proj-ssh-test"}
	knownPath, err := confine.PrepareSSHKnownHosts(c)
	testutil.FailErr(t, "PrepareSSHKnownHosts", err)

	bannerOnly := gitSSHLaunchInput{
		commandLine: "git fetch origin",
		dest:        "git.example.test:22",
		host:        "git.example.test",
		port:        22,
		mediated:    true,
		confinement: &c,
		exitCode:    128,
		failed:      true,
		transport: confine.GitTransportObservation{
			Cause: confine.GitTransportCauseEstablished,
			Attempts: []confine.GitTransportAttempt{{
				Ordinal: 1, Destination: "git.example.test:22", Host: "git.example.test", Port: 22,
				Outcome: "established", SawServerBanner: true, ReachedUserAuth: false,
			}},
		},
	}
	obs, _, suppress := evaluateGitSSHRecovery(t.Context(), bannerOnly)
	if obs == nil || obs.Cause != confine.SSHFailureHostKeyUnverified || !suppress {
		t.Fatalf("unverified host key = %+v suppress=%v", obs, suppress)
	}

	line := "git.example.test ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAILM+rvN+ot98qgEN796jTiQfZfG1KaT0PtFDJ/XFSqti\n"
	testutil.FailErr(t, "write known_hosts", os.WriteFile(knownPath, []byte(line), 0o600))
	obs, _, _ = evaluateGitSSHRecovery(t.Context(), bannerOnly)
	if obs == nil || obs.Cause != confine.SSHFailureHostKeyChanged || obs.TrustPath != knownPath {
		t.Fatalf("changed host key = %+v, want trust path %q", obs, knownPath)
	}

	noBanner := bannerOnly
	noBanner.transport = confine.GitTransportObservation{
		Cause: confine.GitTransportCauseEstablished,
		Attempts: []confine.GitTransportAttempt{{
			Ordinal: 1, Host: "git.example.test", Port: 22, Outcome: "established",
		}},
	}
	if obs, _, _ := evaluateGitSSHRecovery(t.Context(), noBanner); obs != nil {
		t.Fatalf("pre-banner EOF misclassified as %+v", obs)
	}

	userAuthNoAgent := bannerOnly
	userAuthNoAgent.diagnostics = []byte("Permission denied (publickey).\n")
	userAuthNoAgent.transport = confine.GitTransportObservation{
		Cause: confine.GitTransportCauseEstablished,
		Attempts: []confine.GitTransportAttempt{{
			Ordinal: 1, Destination: "git.example.test:22", Host: "git.example.test", Port: 22,
			Outcome: "established", SawServerBanner: true, ReachedUserAuth: true,
		}},
	}
	obs, refusals, _ := evaluateGitSSHRecovery(noAgentCtx, userAuthNoAgent)
	if obs == nil || obs.Cause != confine.SSHFailureAgentUnavailable || len(refusals) != 0 {
		t.Fatalf("missing host agent = obs=%+v refusals=%+v", obs, refusals)
	}
}

func TestEvaluateDirectGitSSHANDResolverSockets(t *testing.T) {
	if !isSystemResolverSocket("/var/run/mDNSResponder") || isSystemResolverSocket("/tmp/ssh-agent.sock") {
		t.Fatal("unexpected isSystemResolverSocket classification")
	}

	if obs, _, suppress := evaluateDirectGitSSH(t.Context(), gitSSHLaunchInput{exitCode: 1, failed: true}); obs != nil || !suppress {
		t.Fatalf("non-128 direct SSH = %+v suppress=%v", obs, suppress)
	}

	testutil.RequireLineage(t, lineage.Supported())
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	sshDir := filepath.Join(home, ".ssh")
	testutil.FailErr(t, "mkdir .ssh", os.MkdirAll(sshDir, 0o700))
	knownHosts := filepath.Join(sshDir, "known_hosts")
	testutil.FailErr(t, "write home known_hosts", os.WriteFile(knownHosts, []byte("direct.example.test ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAILM+rvN+ot98qgEN796jTiQfZfG1KaT0PtFDJ/XFSqti\n"), 0o600))

	sock := shortSocket(t)
	t.Setenv("SSH_AUTH_SOCK", sock)

	unreadableConf := &confine.Confinement{Network: confine.NetworkDirectIP, ProjectID: "proj-direct", ReadDenyPaths: []string{sshDir}}
	unreadableLease, err := confine.BindAction(unreadableConf, confine.EgressCommand{ProjectID: "proj-direct", SessionID: "s1"})
	testutil.FailErr(t, "bind direct action", err)
	defer unreadableLease.Close(t.Context())

	in := gitSSHLaunchInput{
		commandLine: "git push origin main",
		dest:        "direct.example.test:22",
		host:        "direct.example.test",
		port:        22,
		confinement: unreadableConf,
		lease:       unreadableLease,
		exitCode:    128,
		failed:      true,
		diagnostics: []byte("Permission denied (publickey).\n"),
	}
	obs, refusals, _ := evaluateDirectGitSSH(t.Context(), in)
	if obs == nil || obs.Cause != confine.SSHFailureHostKeyUnreadable || len(refusals) != 2 {
		t.Fatalf("unreadable direct trust = obs=%+v refusals=%+v", obs, refusals)
	}

	readableConf := &confine.Confinement{HostExecution: true, Network: confine.NetworkDirectIP, ProjectID: "proj-direct-2"}
	readableLease, err := confine.BindAction(readableConf, confine.EgressCommand{ProjectID: "proj-direct-2", SessionID: "s1"})
	testutil.FailErr(t, "bind readable direct action", err)
	defer readableLease.Close(t.Context())

	in.confinement = readableConf
	in.lease = readableLease
	obs, refusals, _ = evaluateDirectGitSSH(t.Context(), in)
	if obs == nil || obs.Cause != confine.SSHFailureAgentRequired || len(refusals) != 1 {
		t.Fatalf("direct agent required = obs=%+v refusals=%+v", obs, refusals)
	}

	in.hasAgent = true
	in.agentPath = sock
	obs, refusals, _ = evaluateDirectGitSSH(t.Context(), in)
	if obs == nil || obs.Cause != confine.SSHFailurePublicKeyRejected || len(refusals) != 0 {
		t.Fatalf("direct publickey rejected = obs=%+v refusals=%+v", obs, refusals)
	}

	in.hasAgent = false
	noAgentCtx := isolateWithoutHostAgent(t)
	obs, refusals, _ = evaluateDirectGitSSH(noAgentCtx, in)
	if obs == nil || obs.Cause != confine.SSHFailureAgentUnavailable || len(refusals) != 0 {
		t.Fatalf("direct agent unavailable = obs=%+v refusals=%+v", obs, refusals)
	}
}
