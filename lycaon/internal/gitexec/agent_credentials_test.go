//go:build unix

package gitexec

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	execpkg "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/gitcredential"
	"github.com/lycaon/lycaon/internal/testutil"
)

type memoryStore struct {
	mu     sync.Mutex
	saved  map[string]gitcredential.Credential
	writes int
}

func (m *memoryStore) Get(c gitcredential.Credential) (gitcredential.Credential, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	found, ok := m.saved[c.Protocol+"://"+c.Host]
	return found, ok, nil
}

func (m *memoryStore) Store(gitcredential.Credential) error {
	m.mu.Lock()
	m.writes++
	m.mu.Unlock()
	return nil
}
func (m *memoryStore) Erase(gitcredential.Credential) error {
	m.mu.Lock()
	m.writes++
	m.mu.Unlock()
	return nil
}

// authenticatedRemote serves upload-pack advertisements only to alice and
// records every Authorization header it sees.
func authenticatedRemote(t *testing.T, bin, repo string) (*httptest.Server, *[]string) {
	t.Helper()
	var mu sync.Mutex
	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("Authorization"))
		mu.Unlock()
		if user, password, ok := r.BasicAuth(); !ok || user != "alice" || password != "remote-secret" {
			w.Header().Set("WWW-Authenticate", `Basic realm="fixture"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
		pkt := "# service=git-upload-pack\n"
		_, _ = fmt.Fprintf(w, "%04x%s0000", len(pkt)+4, pkt)
		cmd := exec.Command(bin, "upload-pack", "--stateless-rpc", "--advertise-refs", repo)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		cmd.Stdout = w
		_ = cmd.Run()
	}))
	t.Cleanup(server.Close)
	return server, &seen
}

// An agent network command's Git reaches credentials only through its scoped
// service: an admitted recipient authenticates once its credential is
// released, a declined one is recorded, an unadmitted one receives nothing,
// and the user's store is never written.
func TestAgentCredentialsReachOnlyAdmittedRecipients(t *testing.T) {
	bin := useBundledGit(t)
	self, err := os.Executable()
	testutil.FailErr(t, "resolve test binary", err)
	helper := filepath.Join(t.TempDir(), "helper")
	script := fmt.Sprintf("#!/bin/sh\nPW_GITEXEC_HELPER=1 exec %q -test.run='^TestCredentialHelperProcess$' -- \"$@\"\n", self)
	testutil.FailErr(t, "write helper", os.WriteFile(helper, []byte(script), 0o755))
	previousExecutable, previousStore := executablePath, agentCredentialStore
	executablePath = func() (string, error) { return helper, nil }
	t.Cleanup(func() { executablePath, agentCredentialStore = previousExecutable, previousStore })

	repo := t.TempDir()
	initRepo(t, repo)
	server, seen := authenticatedRemote(t, bin, repo)
	host := strings.TrimPrefix(server.URL, "http://")
	store := &memoryStore{saved: map[string]gitcredential.Credential{
		"http://" + host: {Protocol: "http", Host: host, Username: "alice", Password: "remote-secret"},
	}}
	agentCredentialStore = func() gitcredential.Backend { return store }

	var declined []string
	run := func(admit, released bool) (string, int) {
		credentials, err := StartAgentCredentials(t.Context(), func(gitcredential.Recipient, gitcredential.Credential) bool { return released })
		testutil.FailErr(t, "start credentials", err)
		defer func() { declined = credentials.Declined(); _ = credentials.Close() }()
		if admit {
			credentials.Admit(server.URL + "/repo.git")
		}
		ctx := WithAgentLaunch(AgentEntry(t.Context()), AgentLaunch{Plan: execpkg.HostLaunch("git"), Credentials: credentials})
		work := t.TempDir()
		initRepo(t, work)
		out, code, err := Run(ctx, work, []string{"ls-remote", server.URL + "/repo.git"}, Opts{Profile: ProfileNetwork})
		testutil.FailErr(t, "ls-remote", err)
		return string(out), code
	}

	if out, code := run(false, true); code == 0 {
		t.Fatalf("unadmitted recipient authenticated: %s", out)
	}
	if out, code := run(true, false); code == 0 || len(declined) != 1 || declined[0] != server.URL {
		t.Fatalf("a declined recipient: code %d, declined %v, %s", code, declined, out)
	}
	for _, header := range *seen {
		if header != "" {
			t.Fatalf("credentials reached an unadmitted or declined recipient: %q", header)
		}
	}
	if out, code := run(true, true); code != 0 || len(declined) != 0 {
		t.Fatalf("admitted, released recipient failed: code %d, declined %v, %s", code, declined, out)
	}
	if store.writes != 0 {
		t.Fatalf("agent Git wrote the user's credential store %d times", store.writes)
	}
}

// writeCredentialHelperScript writes an executable helper that drains its
// request and answers with a fixed credential.
func writeCredentialHelperScript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "helper")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	return path
}

func TestUserHelpersGetRunsConfiguredHelper(t *testing.T) {
	useBundledGit(t)
	helper := writeCredentialHelperScript(t, "cat > /dev/null\necho username=alice\necho password=helper-secret\n")
	executablePath = func() (string, error) { return helper, nil }
	SetHostConfig(stubHostConfig{helper: "osxkeychain"})

	got, ok, err := (userHelpers{}).Get(gitcredential.Credential{Protocol: "https", Host: "example.com", Path: "repo.git"})
	if err != nil || !ok {
		t.Fatalf("Get = (%+v, %v, %v)", got, ok, err)
	}
	if got.Username != "alice" || got.Password != "helper-secret" {
		t.Fatalf("credential = %+v", got)
	}
}

func TestUserHelpersGetFailsClosedWithoutHelper(t *testing.T) {
	useBundledGit(t)
	SetHostConfig(nil)

	got, ok, err := (userHelpers{}).Get(gitcredential.Credential{Protocol: "https", Host: "example.com"})
	if err != nil || ok {
		t.Fatalf("Get without a helper = (%+v, %v, %v)", got, ok, err)
	}
	if got != (gitcredential.Credential{}) {
		t.Fatalf("credential = %+v", got)
	}
}

func TestUserHelpersStoreAndErase(t *testing.T) {
	useBundledGit(t)

	cred := gitcredential.Credential{Protocol: "https", Host: "example.com", Username: "alice", Password: "helper-secret"}
	if err := (userHelpers{}).Store(cred); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if err := (userHelpers{}).Erase(cred); err != nil {
		t.Fatalf("Erase: %v", err)
	}
}

// TestCredentialHelperProcess is the helper verb Git runs, forwarding to the
// service named in its environment.
func TestCredentialHelperProcess(t *testing.T) {
	if os.Getenv("PW_GITEXEC_HELPER") != "1" {
		return
	}
	args := os.Args
	for i, arg := range args {
		if arg == "--" {
			args = args[i+1:]
			break
		}
	}
	if len(args) != 2 || args[0] != string(hostHelperVault) {
		os.Exit(2)
	}
	if err := gitcredential.Forward(args[1], os.Getenv(gitcredential.SocketEnv), os.Stdin, os.Stdout); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}
