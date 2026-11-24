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

	"github.com/lycaon/lycaon/internal/egressproxy"
	execpkg "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/sshproxy"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestAgentSSHBindsFromNetworkStartHookAndCleansUp(t *testing.T) {
	e := productionExecution()
	useBundledGit(t)
	self, err := os.Executable()
	testutil.FailErr(t, "resolve test binary", err)

	var dials atomic.Int32
	sshService, err := e.startAgentSSH(t.Context(), func(_ context.Context, ep egressproxy.Endpoint) (net.Conn, sshproxy.ReplyCode, error) {
		dials.Add(1)
		if ep.Host != "ssh.example.com" || ep.Port != 2222 {
			t.Errorf("unexpected endpoint %+v", ep)
		}
		server, client := net.Pipe()
		go func() {
			defer func() { _ = server.Close() }()
			// Close cleanly so git ls-remote fails fast after verifying transport setup.
		}()
		return client, sshproxy.ReplySuccess, nil
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
