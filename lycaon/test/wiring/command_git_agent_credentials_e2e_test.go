//go:build unix

package wiring

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/gitcredential"
	"github.com/lycaon/lycaon/internal/gitengine"
	"github.com/lycaon/lycaon/internal/gitexec"
	"github.com/lycaon/lycaon/internal/socketref"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
)

type fixedCredentialStore struct{ username, password string }

func (s fixedCredentialStore) Get(c gitcredential.Credential) (gitcredential.Credential, bool, error) {
	c.Username, c.Password = s.username, s.password
	return c, true, nil
}
func (fixedCredentialStore) Store(gitcredential.Credential) error { return nil }
func (fixedCredentialStore) Erase(gitcredential.Credential) error { return nil }

// authenticatedUploadPack serves a repository's refs only to the fixture
// user and records whether any request carried its credentials.
func authenticatedUploadPack(t *testing.T, repo string) (*httptest.Server, func() bool) {
	t.Helper()
	bin, err := gitengine.BinaryPath()
	testutil.FailErr(t, "resolve git", err)
	var mu sync.Mutex
	authenticated := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user, password, ok := r.BasicAuth(); !ok || user != "alice" || password != "remote-secret" {
			w.Header().Set("WWW-Authenticate", `Basic realm="fixture"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		mu.Lock()
		authenticated = true
		mu.Unlock()
		w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
		pkt := "# service=git-upload-pack\n"
		_, _ = fmt.Fprintf(w, "%04x%s0000", len(pkt)+4, pkt)
		cmd := exec.Command(bin, "upload-pack", "--stateless-rpc", "--advertise-refs", repo)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		cmd.Stdout = w
		_ = cmd.Run()
	}))
	t.Cleanup(server.Close)
	return server, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return authenticated
	}
}

// An agent's Git authenticates through the host credential service from
// inside its real confinement: the confined helper reaches the service and
// the service verifies it as the served Git's own helper.
func TestAgentGitAuthenticatesFromInsideItsConfinement(t *testing.T) {
	gitengine.TestingEnableBundledBinary()
	confine.TestingSetAutoConfine(t)
	t.Setenv("LYCAON_SANDBOX", "on")
	t.Cleanup(gitexec.TestingUseAgentCredentialStore(fixedCredentialStore{username: "alice", password: "remote-secret"}))
	remote := t.TempDir()
	testutil.FailErr(t, "seed remote", os.WriteFile(filepath.Join(remote, "README"), []byte("remote\n"), 0o644))
	gittest.InitCommit(t, remote, "seed")
	head := strings.TrimSpace(gittest.Run(t, remote, "rev-parse", "HEAD"))
	server, authenticated := authenticatedUploadPack(t, remote)
	parsed, err := url.Parse(server.URL)
	testutil.FailErr(t, "parse server", err)
	port, err := strconv.ParseUint(parsed.Port(), 10, 16)
	testutil.FailErr(t, "parse port", err)

	s := newCommandProgramSession(t, "git ls-remote "+server.URL+"/repo.git", "")
	gittest.InitCommit(t, s.dir, "work")
	if !s.h.Server.Admin.Capabilities.Installation.Loopback.GrantChat(s.session, []uint16{uint16(port)}, "git-fixture", "test-listener", nil) {
		t.Fatal("loopback grant failed")
	}
	runCommandWithApprovals(t, s)
	got := s.outcome(t, "program-1")
	t.Logf("ls-remote stderr: %s", got.Stderr)
	if got.Status != 0 || !strings.Contains(got.Stdout, head) {
		t.Fatalf("ls-remote under confinement = %+v", got)
	}
	if !authenticated() {
		t.Fatal("the remote never saw the stored credentials")
	}
}

// fakeSSHAgent answers identity requests with no keys and counts them.
func fakeSSHAgent(t *testing.T) (string, func() int) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "pwa-") //nolint:usetesting // A socket path must stay short.
	testutil.FailErr(t, "agent dir", err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "agent.sock")
	listener, err := net.Listen("unix", path)
	testutil.FailErr(t, "listen agent", err)
	t.Cleanup(func() { _ = listener.Close() })
	var requests atomic.Int32
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			header := make([]byte, 5)
			if _, err := io.ReadFull(conn, header); err == nil && header[4] == 11 {
				requests.Add(1)
				_, _ = conn.Write([]byte{0, 0, 0, 5, 12, 0, 0, 0, 0})
			}
			_ = conn.Close()
		}
	}()
	return path, func() int { return int(requests.Load()) }
}

// An agent's Git reaches the user's SSH agent from inside its confinement
// once the session holds a socket capability for it, and not before.
func TestAgentGitReachesTheGrantedSSHAgentFromInsideItsConfinement(t *testing.T) {
	gitengine.TestingEnableBundledBinary()
	confine.TestingSetAutoConfine(t)
	t.Setenv("LYCAON_SANDBOX", "on")
	agent, requests := fakeSSHAgent(t)
	t.Setenv("SSH_AUTH_SOCK", agent)
	self, err := os.Executable()
	testutil.FailErr(t, "resolve test binary", err)
	shims := t.TempDir()
	testutil.FailErr(t, "write ssh shim", os.WriteFile(filepath.Join(shims, "ssh"), []byte("#!/bin/sh\nexec '"+self+"' "+sshAgentProbeVerb+" \"$@\"\n"), 0o755))
	t.Setenv("PATH", shims+string(os.PathListSeparator)+os.Getenv("PATH"))
	remote := "ssh://git@ssh.example.test/org/repo.git"

	ungranted := newCommandProgramSession(t, "git ls-remote "+remote, "")
	gittest.InitCommit(t, ungranted.dir, "work")
	runCommandWithApprovals(t, ungranted, "recommended")
	if got := ungranted.outcome(t, "program-1"); strings.Contains(got.Stderr, sshAgentAnswered) || requests() != 0 {
		t.Fatalf("SSH agent reached Git without a capability: %d requests, %+v", requests(), got)
	}

	granted := newCommandProgramSession(t, "git ls-remote "+remote, "")
	gittest.InitCommit(t, granted.dir, "work")
	lease, err := socketref.Capture(agent)
	testutil.FailErr(t, "capture agent", err)
	t.Cleanup(lease.Close)
	grant := confine.SocketGrant{ApprovedPath: agent, ResolvedPath: fspath.CanonicalPath(agent), Binding: lease.Binding()}
	if !granted.h.Server.Admin.Capabilities.Installation.Sockets.GrantChat(granted.session, grant, "ssh-agent", "test-agent", "", nil) {
		t.Fatal("socket grant failed")
	}
	runCommandWithApprovals(t, granted, "recommended")
	got := granted.outcome(t, "program-1")
	if !strings.Contains(got.Stderr, sshAgentAnswered) || requests() == 0 {
		t.Fatalf("granted SSH agent unreachable from the confined Git: %d requests, %+v", requests(), got)
	}
}
