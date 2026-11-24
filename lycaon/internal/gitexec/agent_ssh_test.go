//go:build unix

package gitexec

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/egressproxy"
	execpkg "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/lineage"
	"github.com/lycaon/lycaon/internal/sshproxy"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestAgentSSHBindsFromNetworkStartHookAndCleansUp(t *testing.T) {
	e := productionExecution()
	useBundledGit(t)
	self, err := os.Executable()
	testutil.FailErr(t, "resolve test binary", err)

	var dials atomic.Int32
	sshService, err := e.startAgentSSH(t.Context(), func(_ context.Context, ep egressproxy.Endpoint) (sshproxy.DialResult, error) {
		dials.Add(1)
		if ep.Host != "ssh.example.com" || ep.Port != 2222 {
			t.Errorf("unexpected endpoint %+v", ep)
		}
		server, client := net.Pipe()
		go func() {
			defer func() { _ = server.Close() }()
			// Close cleanly so git ls-remote fails fast after verifying transport setup.
		}()
		return sshproxy.DialResult{Conn: client, Reply: sshproxy.ReplySuccess, Stage: sshproxy.StageDial, Outcome: sshproxy.OutcomeEstablished}, nil
	})
	testutil.FailErr(t, "start agent ssh", err)
	socketDir := filepath.Dir(sshService.Socket())

	var recordedPID atomic.Int32
	plan := execpkg.HostLaunch("git")
	plan.RecordStart = func(pid int) error {
		recordedPID.Store(int32(pid))
		return nil
	}

	proxyScript := filepath.Join(t.TempDir(), "ssh-helper")
	script := fmt.Sprintf("#!/bin/sh\nPW_GITEXEC_SSH_HELPER=1 exec %q -test.run='^TestGitExecSSHHelperProcess$' -- \"$@\"\n", self)
	testutil.FailErr(t, "write ssh helper", os.WriteFile(proxyScript, []byte(script), 0o755))

	ctx := WithAgentLaunch(AgentEntry(t.Context()), AgentLaunch{Plan: plan, SSH: sshService})
	work := t.TempDir()
	initRepo(t, work)
	sshCmd := fmt.Sprintf("ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ProxyCommand='%s %%h %%p'", proxyScript)

	_, code, err := e.run(ctx, work, []string{"ls-remote", "ssh://git@ssh.example.com:2222/repo.git"}, Opts{
		Profile:     ProfileNetwork,
		ExtraConfig: []string{"core.sshCommand=" + sshCmd},
	}, nil)
	testutil.FailErr(t, "run network git", err)
	if code == 0 {
		t.Fatal("closed pipe unexpectedly completed ls-remote")
	}
	if dials.Load() != 1 {
		t.Fatalf("dials = %d, want 1", dials.Load())
	}
	if recordedPID.Load() == 0 {
		t.Fatal("existing RecordStart hook did not run")
	}
	if sshService.BindError() != nil {
		t.Fatalf("unexpected BindError: %v", sshService.BindError())
	}

	// Credentials and SSH sockets occupy distinct service directories.
	creds, err := e.startAgentCredentials(t.Context(), nil, &memoryStore{})
	testutil.FailErr(t, "start credentials", err)
	if filepath.Dir(creds.Socket()) == socketDir || creds.Socket() == sshService.Socket() {
		t.Fatalf("SSH socket %q collided with credential socket %q", sshService.Socket(), creds.Socket())
	}
	_ = creds.Close()

	testutil.FailErr(t, "close ssh service", sshService.Close())
	testutil.FailErr(t, "repeat close ssh service", sshService.Close())
	if _, err := os.Stat(socketDir); !os.IsNotExist(err) {
		t.Fatalf("ssh service directory remained after close: %v", err)
	}
}

func TestGitExecSSHHelperProcess(t *testing.T) {
	if os.Getenv("PW_GITEXEC_SSH_HELPER") != "1" {
		return
	}
	os.Exit(runGitExecSSHHelper())
}

func runGitExecSSHHelper() int {
	args := os.Args
	for i, arg := range args {
		if arg == "--" {
			args = args[i+1:]
			break
		}
	}
	if len(args) != 2 {
		return 2
	}
	conn, _, err := sshproxy.Connect(context.Background(), os.Getenv(sshproxy.SocketEnv), strings.TrimSpace(args[0]), strings.TrimSpace(args[1]))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer func() { _ = conn.Close() }()
	go func() {
		_, _ = io.Copy(conn, os.Stdin)
		_ = conn.CloseWrite()
	}()
	_, _ = io.Copy(os.Stdout, conn)
	return 0
}

func TestAgentSSHObservationAndBindError(t *testing.T) {
	var nilSSH *AgentSSH
	if obs := nilSSH.Observation("exec-nil", "git push", 1, true, "", "", 0); obs.Cause != "" || len(obs.Attempts) != 0 {
		t.Fatalf("nil Observation = %+v, want zero", obs)
	}

	useBundledGit(t)
	bindFailedSSH, err := StartAgentSSH(t.Context(), nil)
	testutil.FailErr(t, "start bind-failed ssh", err)
	defer func() { _ = bindFailedSSH.Close() }()
	if bindFailedSSH.HasDialer() {
		t.Fatal("unbound AgentSSH unexpectedly reported HasDialer")
	}
	_ = bindFailedSSH.HostService()
	if err := bindFailedSSH.Bind(0); err == nil || bindFailedSSH.BindError() == nil {
		t.Fatalf("Bind(0) err=%v BindError=%v, want error", err, bindFailedSSH.BindError())
	}
	setupObs := bindFailedSSH.Observation("exec-bind", "git fetch", 128, true, "git@ssh.example.com:repo.git", "ssh.example.com", 22)
	if setupObs.Stage != "helper_bind" || setupObs.PolicyReason != "helper_bind_failed" {
		t.Fatalf("bind error Observation = %+v, want helper_bind/helper_bind_failed", setupObs)
	}
}

func runBoundAgentSSHCase(t *testing.T, e execution, work, sshCmd, name string, lease *confine.ActionLease, port string) confine.GitTransportObservation {
	t.Helper()
	svc, err := StartAgentSSH(t.Context(), nil)
	testutil.FailErr(t, name+": StartAgentSSH", err)
	defer func() { _ = svc.Close() }()
	svc.BindExecution("exec-"+name, lease)
	if !svc.HasDialer() {
		t.Fatalf("%s: BindExecution did not bind dialer", name)
	}
	ctx := WithAgentLaunch(AgentEntry(t.Context()), AgentLaunch{Plan: execpkg.HostLaunch("git"), SSH: svc})
	remote := "ssh://git@127.0.0.1:" + port + "/repo.git"
	_, _, err = e.run(ctx, work, []string{"ls-remote", remote}, Opts{
		Profile:     ProfileNetwork,
		ExtraConfig: []string{"core.sshCommand=" + sshCmd},
	}, nil)
	testutil.FailErr(t, name+": run git", err)
	return svc.Observation("exec-"+name, "git ls-remote", 128, true, remote, "127.0.0.1", 22)
}

func TestAgentSSHBindExecutionDispositions(t *testing.T) {
	e := productionExecution()
	useBundledGit(t)
	testutil.RequireLineage(t, lineage.Supported())
	self, err := os.Executable()
	testutil.FailErr(t, "resolve executable", err)

	_, err = confine.StartEgressBroker(t.TempDir(), net.Listen)
	testutil.FailErr(t, "start egress broker", err)
	t.Cleanup(func() { _ = confine.StopEgressBroker() })

	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	testutil.FailErr(t, "listen upstream", err)
	t.Cleanup(func() { _ = upstream.Close() })
	go func() {
		for {
			c, acceptErr := upstream.Accept()
			if acceptErr != nil {
				return
			}
			_ = c.Close()
		}
	}()
	upPort := fmt.Sprintf("%d", upstream.Addr().(*net.TCPAddr).Port)

	proxyScript := filepath.Join(t.TempDir(), "ssh-helper")
	script := fmt.Sprintf("#!/bin/sh\nPW_GITEXEC_SSH_HELPER=1 exec %q -test.run='^TestGitExecSSHHelperProcess$' -- \"$@\"\n", self)
	testutil.FailErr(t, "write ssh helper", os.WriteFile(proxyScript, []byte(script), 0o755))
	sshCmd := fmt.Sprintf("ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ProxyCommand='%s %%h %%p'", proxyScript)
	work := t.TempDir()
	initRepo(t, work)

	if obs := runBoundAgentSSHCase(t, e, work, sshCmd, "lease-ended", nil, upPort); len(obs.Attempts) == 0 || obs.Attempts[0].Outcome != sshproxy.OutcomeLeaseEnded {
		t.Fatalf("lease-ended observation = %+v", obs)
	}
	deniedConf := &confine.Confinement{Network: confine.NetworkProxyOnly, ProjectID: "proj-deny", LoopbackConnect: false}
	deniedLease, err := confine.BindAction(deniedConf, confine.EgressCommand{ProjectID: "proj-deny", SessionID: "sess-1"})
	testutil.FailErr(t, "bind denied action", err)
	defer deniedLease.Close(t.Context())
	if obs := runBoundAgentSSHCase(t, e, work, sshCmd, "denied", deniedLease, upPort); len(obs.Attempts) == 0 || obs.Attempts[0].Outcome != sshproxy.OutcomeEndpointDenied {
		t.Fatalf("denied observation = %+v", obs)
	}
	allowedConf := &confine.Confinement{Network: confine.NetworkProxyOnly, ProjectID: "proj-allow", LoopbackConnect: true}
	confine.SetEgressResolver(func(context.Context, confine.EgressCommand, egressproxy.Endpoint, *confine.EgressDetectionCitation) bool {
		return true
	})
	t.Cleanup(func() { confine.SetEgressResolver(nil) })
	allowedLease, err := confine.BindAction(allowedConf, confine.EgressCommand{ProjectID: "proj-allow", SessionID: "sess-1"})
	testutil.FailErr(t, "bind allowed action", err)
	defer allowedLease.Close(t.Context())
	if obs := runBoundAgentSSHCase(t, e, work, sshCmd, "established", allowedLease, upPort); len(obs.Attempts) == 0 || obs.Attempts[0].Outcome != sshproxy.OutcomeEstablished {
		t.Fatalf("established observation = %+v", obs)
	}
	closedLn, err := net.Listen("tcp", "127.0.0.1:0")
	testutil.FailErr(t, "listen closed port", err)
	closedPort := fmt.Sprintf("%d", closedLn.Addr().(*net.TCPAddr).Port)
	_ = closedLn.Close()
	if obs := runBoundAgentSSHCase(t, e, work, sshCmd, "dial-failed", allowedLease, closedPort); len(obs.Attempts) == 0 || obs.Attempts[0].Outcome != sshproxy.OutcomeDialFailed {
		t.Fatalf("dial-failed observation = %+v", obs)
	}
}
