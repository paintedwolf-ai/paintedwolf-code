package gitcmd

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/egressproxy"
	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/gitcredential"
	"github.com/lycaon/lycaon/internal/gitexec"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

const (
	fixtureCredentialUser     = "alice"
	fixtureCredentialPassword = "remote-secret"
)

// askResult is one review's answer.
type askResult struct {
	decision secretmatch.Decision
	err      error
}

// TestReleaseCredentialReviewsEachSavedRelease drives the credential review
// for one saved credential: the guards keep it without asking, a faulting or
// declining review keeps it, and only sending it unchanged releases it.
func TestReleaseCredentialReviewsEachSavedRelease(t *testing.T) {
	recipient := gitexec.Recipient{Protocol: "https", Host: "Example.test", Path: "org/repo.git"}

	cases := []struct {
		name     string
		withAsk  bool
		matcher  *secretmatch.Matcher
		ask      askResult
		password string
		want     bool
		wantAsk  bool
	}{
		{name: "a missing review keeps the credential", matcher: fingerprinterMatcher(t), password: fixtureCredentialPassword},
		{name: "a missing matcher keeps the credential", withAsk: true, ask: askResult{decision: secretmatch.SendUnchanged}, password: fixtureCredentialPassword},
		{name: "an empty credential is never reviewed", withAsk: true, matcher: fingerprinterMatcher(t), ask: askResult{decision: secretmatch.SendUnchanged}},
		{name: "a matcher without fingerprinting keeps the credential", withAsk: true, matcher: secretmatch.NewInertMatcher(), ask: askResult{decision: secretmatch.SendUnchanged}, password: fixtureCredentialPassword},
		{name: "a faulting review keeps the credential", withAsk: true, matcher: fingerprinterMatcher(t), ask: askResult{decision: secretmatch.SendUnchanged, err: errors.New("review unavailable")}, password: fixtureCredentialPassword, wantAsk: true},
		{name: "a withheld decision keeps the credential", withAsk: true, matcher: fingerprinterMatcher(t), ask: askResult{decision: secretmatch.Withhold}, password: fixtureCredentialPassword, wantAsk: true},
		{name: "a redacted decision keeps the credential", withAsk: true, matcher: fingerprinterMatcher(t), ask: askResult{decision: secretmatch.SendRedacted}, password: fixtureCredentialPassword, wantAsk: true},
		{name: "sending unchanged releases the credential", withAsk: true, matcher: fingerprinterMatcher(t), ask: askResult{decision: secretmatch.SendUnchanged}, password: fixtureCredentialPassword, want: true, wantAsk: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ask, seen := recordingAsk(tc.ask)
			owners := &Owners{SecretMatcher: tc.matcher}
			if tc.withAsk {
				owners.SecretAsk = ask
			}
			credential := gitexec.Credential{Protocol: "https", Host: "example.test", Username: fixtureCredentialUser, Password: tc.password}
			if got := owners.releaseCredential(t.Context(), recipient, credential); got != tc.want {
				t.Fatalf("releaseCredential: want %v, got %v", tc.want, got)
			}
			alert := seen()
			if tc.wantAsk != (alert != nil) {
				t.Fatalf("review asked: want %v, got alert %+v", tc.wantAsk, alert)
			}
			if alert == nil {
				return
			}
			if alert.Surface != secretmatch.SurfaceCommand || alert.RuleID != secretmatch.ManagedRuleID ||
				alert.DestinationID == "" || alert.DestinationLabel != "https://example.test:443" ||
				!slices.Equal(alert.SecretNames, []string{savedCredentialShape}) ||
				alert.RuleTitle != savedCredentialShape || alert.GenericShape != savedCredentialShape ||
				alert.Occurrences != 1 || alert.SourceKind != secretmatch.SourceToolArgument ||
				alert.SourceTool != "git" || alert.SourcePath != "your saved Git credentials" ||
				alert.OriginKind != secretmatch.OriginField || alert.Container != savedCredentialShape ||
				len(alert.Fingerprints) != 1 || alert.Fingerprints[0] == "" {
				t.Fatalf("unexpected review alert: %+v", alert)
			}
		})
	}
}

// TestCredentialDeniedNamesTheKeptRecipient checks the refusal a network
// command settles with: it names the first kept recipient by its origin, or
// by its raw URL when that URL cannot be parsed.
func TestCredentialDeniedNamesTheKeptRecipient(t *testing.T) {
	cases := []struct {
		name     string
		declined []string
		wantHost string
	}{
		{name: "nothing was declined"},
		{name: "a parseable recipient is named by its origin", declined: []string{"https://Example.test:8443/org/repo.git"}, wantHost: "https://example.test:8443"},
		{name: "an unparseable recipient keeps its raw URL", declined: []string{"http://[::1"}, wantHost: "http://[::1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reject := credentialDenied("ls-remote", tc.declined)
			if tc.declined == nil {
				if reject != nil {
					t.Fatalf("want no refusal, got %+v", reject)
				}
				return
			}
			if reject == nil || reject.Code != toolrejection.OutboundSecretDeniedCode {
				t.Fatalf("want %s refusal, got %+v", toolrejection.OutboundSecretDeniedCode, reject)
			}
			if reject.Data["host"] != tc.wantHost || reject.Data["surface"] != "command" ||
				reject.Data["rule_id"] != secretmatch.ManagedRuleID || reject.Data["shape"] != savedCredentialShape ||
				reject.Data["command"] != "ls-remote" {
				t.Fatalf("unexpected refusal data: %+v", reject.Data)
			}
		})
	}
}

// TestWithheldAgentRefusesTheUngrantedAgent checks the socket refusal a
// failed SSH network command settles with when the user's agent was never
// granted, and that HTTP remotes and granted agents never refuse.
func TestWithheldAgentRefusesTheUngrantedAgent(t *testing.T) {
	socket := shortSocket(t)
	t.Setenv("SSH_AUTH_SOCK", socket)
	sshURL := "ssh://git@example.test/org/repo.git"

	if refusal := withheldAgent(t.Context(), []string{"https://example.test/org/repo.git"}, false); refusal != nil {
		t.Fatalf("an HTTP remote needs no agent: %+v", refusal)
	}
	if refusal := withheldAgent(t.Context(), []string{sshURL}, true); refusal != nil {
		t.Fatalf("a granted agent never refuses: %+v", refusal)
	}
	refusal := withheldAgent(t.Context(), []string{sshURL}, false)
	if refusal == nil || len(refusal) != 1 {
		t.Fatalf("want one ungranted agent refusal, got %+v", refusal)
	}
	if refusal[0].Operation != "network-outbound" || refusal[0].Process != "ssh" ||
		refusal[0].Target != socket || refusal[0].Grant != socket ||
		refusal[0].Count != 1 || refusal[0].Recovery != confine.RecoverSocketPath {
		t.Fatalf("unexpected refusal: %+v", refusal[0])
	}
}

// TestDeclinedCredentialSettlesTheOutboundSecretRefusal runs network commands
// against a remote that requires the saved credential while no review releases
// it: Git fails unauthenticated and every command settles with the outbound
// secret refusal naming the remote, and the credential never reaches it.
// Serial: mutates process-wide egress posture, the credential store, and env.
func TestDeclinedCredentialSettlesTheOutboundSecretRefusal(t *testing.T) {
	h := newHarness(t)
	h.git("reset", "--hard", "HEAD")
	h.git("clean", "-fd")

	bareDir := h.bareRemote(t, "remote.git")
	var served atomic.Bool
	server := httptest.NewServer(serveAuthenticatedUploadPack(h.bin, bareDir, &served))
	t.Cleanup(server.Close)
	remoteURL := server.URL + "/remote.git"

	t.Cleanup(gitexec.TestingUseAgentCredentialStore(fixedCredentialStore{
		username: fixtureCredentialUser, password: fixtureCredentialPassword,
	}))

	confine.SetEgressPosture(confine.PostureAsk)
	t.Cleanup(func() {
		confine.SetEgressPosture(confine.PostureObserve)
		confine.SetEgressResolver(nil)
	})
	confine.SetEgressResolver(func(_ context.Context, _ confine.EgressCommand, ep egressproxy.Endpoint, _ *confine.EgressDetectionCitation) bool {
		return ep.Host == "127.0.0.1" || ep.Host == "localhost"
	})

	h.git("remote", "add", "origin", remoteURL)

	for _, argv := range [][]string{
		{"git", "ls-remote", "origin"},
		{"git", "fetch", "origin"},
		{"git", "pull", "origin", "main"},
		{"git", "clone", remoteURL, "denied-clone"},
	} {
		run := h.exec(argv...)
		run.refused(t, "denied "+strings.Join(argv, " "), toolrejection.OutboundSecretDeniedCode)
		if host, ok := deniedHost(run.err); !ok || host != server.URL {
			t.Fatalf("%v: refusal names %q (ok %v), want %q", argv, host, ok, server.URL)
		}
	}
	if served.Load() {
		t.Fatal("the kept credential reached the remote")
	}
}

// TestALaunchRefusalSettlesTheNetworkCommandAsFailed checks that a launch the
// host refuses settles the network command as a failed producer, not as a
// credential refusal.
func TestALaunchRefusalSettlesTheNetworkCommandAsFailed(t *testing.T) {
	h := newHarness(t)
	origin := h.bareRemote(t, "origin.git")
	h.git("remote", "add", "origin", origin)
	h.owners.Launch = func(context.Context, tools.ToolContext, string, string, []confine.HostService) (exec.LaunchPlan, func(context.Context, tools.AgentLaunchOutcome), error) {
		return exec.LaunchPlan{}, nil, errors.New("launch refused")
	}
	run := h.exec("git", "ls-remote", "origin")
	if run.result.Status != 1 || run.result.Terminal != toolcontract.ProducerFailed || run.err == nil {
		t.Fatalf("want a failed producer, got %+v err %v", run.result, run.err)
	}
}

// fingerprinterMatcher builds a matcher that can fingerprint the fixture
// credential, the way the person's device-key matcher does.
func fingerprinterMatcher(t *testing.T) *secretmatch.Matcher {
	t.Helper()
	fingerprinter, err := secretmatch.NewFingerprinter(bytes.Repeat([]byte{0x5a}, 32))
	testutil.FailErr(t, "fingerprinter", err)
	matcher := &secretmatch.Matcher{}
	matcher.SetFingerprinter(fingerprinter)
	return matcher
}

// recordingAsk returns a review that answers with one result and reads back
// the alert it was shown, if it was asked at all.
func recordingAsk(result askResult) (secretmatch.AskFunc, func() *secretmatch.Alert) {
	var seen *secretmatch.Alert
	return func(_ context.Context, alert secretmatch.Alert) (secretmatch.Resolution, error) {
		seen = &alert
		return secretmatch.Resolution{Decision: result.decision}, result.err
	}, func() *secretmatch.Alert { return seen }
}

// shortSocket creates a listener whose path stays under the unix socket
// length limit, which a session-scoped temp directory can exceed.
func shortSocket(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "pwa-") //nolint:usetesting // A socket path must stay short.
	testutil.FailErr(t, "socket dir", err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	listener, err := net.Listen("unix", filepath.Join(dir, "agent.sock"))
	testutil.FailErr(t, "agent socket", err)
	t.Cleanup(func() { _ = listener.Close() })
	return listener.Addr().String()
}

// deniedHost reads the recipient the outbound secret refusal names.
func deniedHost(err error) (string, bool) {
	reject := toolrejection.AsToolReject(err)
	if reject == nil {
		return "", false
	}
	host, _ := reject.Data["host"].(string)
	return host, true
}

// fixedCredentialStore answers every lookup with one saved credential, the
// way the person's store would for a host Git was sent to before.
type fixedCredentialStore struct {
	username string
	password string
}

func (s fixedCredentialStore) Get(c gitcredential.Credential) (gitcredential.Credential, bool, error) {
	c.Username, c.Password = s.username, s.password
	return c, true, nil
}

func (fixedCredentialStore) Store(gitcredential.Credential) error { return nil }

func (fixedCredentialStore) Erase(gitcredential.Credential) error { return nil }

// serveAuthenticatedUploadPack serves the fixture repository only to the
// fixture user and records whether any request carried those credentials.
func serveAuthenticatedUploadPack(bin, repoDir string, served *atomic.Bool) http.Handler {
	smart := serveSmartHTTP(bin, repoDir)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		if !ok || user != fixtureCredentialUser || password != fixtureCredentialPassword {
			w.Header().Set("WWW-Authenticate", `Basic realm="fixture"`)
			http.Error(w, "credentials required", http.StatusUnauthorized)
			return
		}
		served.Store(true)
		smart.ServeHTTP(w, r)
	})
}
