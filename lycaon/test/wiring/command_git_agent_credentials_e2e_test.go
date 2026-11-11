//go:build unix

package wiring

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/gitcredential"
	"github.com/lycaon/lycaon/internal/gitengine"
	"github.com/lycaon/lycaon/internal/gitexec"
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
