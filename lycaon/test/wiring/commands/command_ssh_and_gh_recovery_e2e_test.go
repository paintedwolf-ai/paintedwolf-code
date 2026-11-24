//go:build unix

package commands

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/godbus/dbus/v5"
	"golang.org/x/crypto/ssh"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/confine/admissiontest"
	"github.com/lycaon/lycaon/internal/detectionpack"
	lycexec "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/gitengine"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/socketref"
	confinefixture "github.com/lycaon/lycaon/internal/testsetup/confine"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type ghFixtureSecret struct {
	Session     dbus.ObjectPath
	Parameters  []byte
	Value       []byte
	ContentType string
}

type ghFixtureSecretService struct{}

func (*ghFixtureSecretService) OpenSession(string, dbus.Variant) (dbus.Variant, dbus.ObjectPath, *dbus.Error) {
	return dbus.MakeVariant(""), "/org/freedesktop/secrets/session/test", nil
}

func (*ghFixtureSecretService) GetSecret(session dbus.ObjectPath) (ghFixtureSecret, *dbus.Error) {
	return ghFixtureSecret{Session: session, Value: []byte("gho_expired_or_rejected_fixture"), ContentType: "text/plain"}, nil
}

func (*ghFixtureSecretService) Close() *dbus.Error { return nil }

func installBundledKeyMaterialFloor(t *testing.T) {
	t.Helper()
	paths, err := detectionpack.BundledKeyMaterialPaths()
	testutil.FailErr(t, "load bundled key-material floor", err)
	prev := confine.SetKeyMaterialPathsSource(func() []string { return paths })
	t.Cleanup(func() { confine.SetKeyMaterialPathsSource(prev) })
}

func setupIsolatedSSHHome(t *testing.T, port uint16, trustedKey ssh.PublicKey) (homeDir, knownHosts, sshConfig, privKey string) {
	t.Helper()
	rawHome, err := os.MkdirTemp("/tmp", "pw-ssh-") //nolint:usetesting // Keep paths under macOS Seatbelt 256-byte kernel log limit.
	testutil.FailErr(t, "mkdir ssh home", err)
	t.Cleanup(func() { _ = os.RemoveAll(rawHome) })
	homeDir = fspath.CanonicalPath(rawHome)
	t.Setenv("HOME", homeDir)
	sshDir := filepath.Join(homeDir, ".ssh")
	testutil.FailErr(t, "mkdir .ssh", os.MkdirAll(sshDir, 0o700))

	knownHosts = filepath.Join(sshDir, "known_hosts")
	sshConfig = filepath.Join(sshDir, "config")
	privKey = filepath.Join(sshDir, "id_ed25519")

	line := fmt.Sprintf("[127.0.0.1]:%d %s", port, ssh.MarshalAuthorizedKey(trustedKey))
	testutil.FailErr(t, "write known_hosts", os.WriteFile(knownHosts, []byte(line), 0o600))
	testutil.FailErr(t, "write ssh config", os.WriteFile(sshConfig, []byte("Host *\n  ServerAliveInterval 30\n"), 0o600))
	testutil.FailErr(t, "write default private key", os.WriteFile(privKey, []byte("-----BEGIN OPENSSH PRIVATE KEY-----\nunrequested-private-key\n-----END OPENSSH PRIVATE KEY-----\n"), 0o600))
	return homeDir, knownHosts, sshConfig, privKey
}

func grantLoopbackPortsOnly(t *testing.T, s *commandProgramSession, ports ...uint16) {
	t.Helper()
	if !s.h.Server.Admin.Capabilities.Installation.Loopback.GrantChat(s.session, ports, "ssh-recovery-fixture", "test-loopback", nil) {
		t.Fatal("loopback grant failed")
	}
}

func startRotatingSSHGitServer(t *testing.T, repoDir string, authorizedUserKey ssh.PublicKey, initialSigner ssh.Signer) (uint16, *atomic.Pointer[ssh.Signer]) {
	t.Helper()
	gitBin, err := gitengine.BinaryPath()
	testutil.FailErr(t, "git binary", err)
	authorizedBlob := authorizedUserKey.Marshal()
	activeSigner := &atomic.Pointer[ssh.Signer]{}
	activeSigner.Store(&initialSigner)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	testutil.FailErr(t, "listen rotating ssh", err)
	t.Cleanup(func() { _ = ln.Close() })
	port := uint16(ln.Addr().(*net.TCPAddr).Port)
	fixture := &sshGitFixture{port: port}

	go func() {
		for {
			rawConn, err := ln.Accept()
			if err != nil {
				return
			}
			cfg := &ssh.ServerConfig{
				PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
					if bytes.Equal(key.Marshal(), authorizedBlob) {
						return &ssh.Permissions{}, nil
					}
					return nil, fmt.Errorf("unauthorized key")
				},
			}
			cfg.AddHostKey(*activeSigner.Load())
			go serveSSHGitConn(rawConn, cfg, gitBin, repoDir, fixture)
		}
	}()
	return port, activeSigner
}

func TestDirectSSHRecoveryMatrixWithoutPrivateKeyGrants(t *testing.T) {
	confinefixture.RequireRefusalReports(t)
	installBundledKeyMaterialFloor(t)
	p := newProgramHost(t)
	admissiontest.Enable(t)
	t.Setenv("LYCAON_SANDBOX", "on")

	agentSock, userPub := startSigningSSHAgent(t)
	t.Setenv("SSH_AUTH_SOCK", agentSock)

	bareDir, _, _ := createBareSeededRepo(t)
	sshFixture := startSSHGitServer(t, bareDir, userPub)
	_, knownHosts, sshConfig, privKey := setupIsolatedSSHHome(t, sshFixture.port, sshFixture.hostPub)

	sshCmd := fmt.Sprintf("ssh -F /dev/null -o UserKnownHostsFile=%q -p %d git@127.0.0.1 echo remote-ok", knownHosts, sshFixture.port)
	defaultSSHCmd := sshCmd
	sockRef, _ := confine.SocketEndpointReferences(agentSock)
	boundSSHCmd := fmt.Sprintf("SSH_AUTH_SOCK=%q %s", sockRef, defaultSSHCmd)

	// 1. Missing known_hosts trust + unbound agent yields one expressible invocation
	// (read_path: [known_hosts] + socket_paths: [agentSock] + {{paintedwolf-socket:...}}), withholding
	// ~/.ssh/id_ed25519 and ~/.ssh/config as probed detail, and succeeds on follow-up without any key grant.
	t.Run("combined trust and agent succeeds without key grant", func(t *testing.T) {
		mock := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{
			{
				Pattern: "run the program",
				ToolCalls: []llm.MockToolCall{{
					ID:   "ssh-attempt-1",
					Name: "command",
					Args: map[string]any{
						"command":            defaultSSHCmd,
						"capability_request": map[string]any{"loopback_connect": map[string]any{"ports": []any{int(sshFixture.port)}}},
					},
				}},
				FollowUpText: "Need trust and agent.",
			},
			{
				Pattern: "retry with exact grants",
				ToolCalls: []llm.MockToolCall{{
					ID:   "ssh-attempt-2",
					Name: "command",
					Args: map[string]any{
						"command": boundSSHCmd,
						"capability_request": map[string]any{
							"loopback_connect": map[string]any{"ports": []any{int(sshFixture.port)}},
							"read_path":        []any{knownHosts},
							"socket_paths":     []any{agentSock},
						},
					},
				}},
				FollowUpText: "Connected.",
			},
		}})
		sess := p.withClient(t, mock, wire.CreateSessionRequest{}, "")
		sess.h.SeedProgress(t, t.Context(), sess.session)
		grantLoopbackPortsOnly(t, sess, sshFixture.port)
		promptApprovingRecommended(t, sess, "run the program")

		raw1 := rawToolResult(t, p.h, sess.session, "ssh-attempt-1")
		for _, want := range []string{
			"SANDBOX_TRY_CAPABILITIES",
			knownHosts,
			agentSock,
			sockRef,
			"`capability_request.read_path`",
			"`capability_request.socket_paths`",
		} {
			if !strings.Contains(raw1, want) {
				t.Fatalf("attempt 1 missing %q in:\n%s", want, raw1)
			}
		}
		// Ensure id_ed25519 is never offered as a requestable read_path grant.
		if strings.Contains(raw1, "this exact path: ["+privKey) || strings.Contains(raw1, "requestable read grants ["+privKey) {
			t.Fatalf("attempt 1 offered private key as requestable grant:\n%s", raw1)
		}
		_ = sshConfig

		// Grant the socket in installation registry (matching user approval) and retry WITHOUT granting privKey.
		grantSSHAuthority(t, sess, agentSock, sshFixture.port)
		promptApprovingRecommended(t, sess, "retry with exact grants")

		out2 := sess.outcome(t, "ssh-attempt-2")
		if out2.Status != 0 || strings.TrimSpace(out2.Stdout) != "remote-ok" {
			t.Fatalf("direct ssh with known_hosts + agent socket failed: status=%d stdout=%q stderr=%q", out2.Status, out2.Stdout, out2.Stderr)
		}
	})

	// 2. Missing trust alone (agent already bound & granted -> SANDBOX_TRY_READ_PATH for known_hosts only).
	t.Run("missing trust alone", func(t *testing.T) {
		sess := p.programWithArgs(t, map[string]any{
			"command": boundSSHCmd,
			"capability_request": map[string]any{
				"loopback_connect": map[string]any{"ports": []any{int(sshFixture.port)}},
				"socket_paths":     []any{agentSock},
			},
		}, wire.CreateSessionRequest{}, "")
		grantSSHAuthority(t, sess, agentSock, sshFixture.port)
		promptApprovingRecommended(t, sess, "run the program")

		raw := rawToolResult(t, p.h, sess.session, "program-1")
		for _, want := range []string{"SANDBOX_TRY_READ_PATH", knownHosts, "`capability_request.read_path`"} {
			if !strings.Contains(raw, want) {
				t.Fatalf("missing trust alone lacked %q in:\n%s", want, raw)
			}
		}
		if strings.Contains(raw, "SANDBOX_TRY_CAPABILITIES") || strings.Contains(raw, "`capability_request.socket_paths`") {
			t.Fatalf("missing trust alone redundantly requested socket grant:\n%s", raw)
		}
	})

	// 3. Missing agent alone (known_hosts granted -> SANDBOX_TRY_SOCKET_PATH, no private key grant).
	t.Run("missing agent alone", func(t *testing.T) {
		sess := p.programWithArgs(t, map[string]any{
			"command": sshCmd,
			"capability_request": map[string]any{
				"loopback_connect": map[string]any{"ports": []any{int(sshFixture.port)}},
				"read_path":        []any{knownHosts},
			},
		}, wire.CreateSessionRequest{}, "")
		grantLoopbackPortsOnly(t, sess, sshFixture.port)
		promptApprovingRecommended(t, sess, "run the program")

		raw := rawToolResult(t, p.h, sess.session, "program-1")
		for _, want := range []string{"SANDBOX_TRY_SOCKET_PATH", agentSock, sockRef, "`capability_request.socket_paths`"} {
			if !strings.Contains(raw, want) {
				t.Fatalf("missing agent alone lacked %q in:\n%s", want, raw)
			}
		}
		if strings.Contains(raw, "SANDBOX_TRY_READ_PATH") || strings.Contains(raw, "`capability_request.read_path`") {
			t.Fatalf("missing agent alone requested private key read_path:\n%s", raw)
		}
	})

	// 4. Pre-auth transport refusal before signing (loopback port NOT granted ->
	// outbound port refusal, zero ~/.ssh speculation).
	t.Run("transport refusal before signing", func(t *testing.T) {
		sess := p.programWithArgs(t, map[string]any{
			"command": defaultSSHCmd,
		}, wire.CreateSessionRequest{}, "")
		promptApprovingRecommended(t, sess, "run the program")

		raw := rawToolResult(t, p.h, sess.session, "program-1")
		for _, forbidden := range []string{"`capability_request.socket_paths`", "`capability_request.read_path`", privKey} {
			if strings.Contains(raw, forbidden) {
				t.Fatalf("pre-auth transport refusal speculated %q in:\n%s", forbidden, raw)
			}
		}
	})

	// 5. Changed host key in direct ssh and mediated git -> SSH_HOST_KEY_CHANGED, preserving StrictHostKeyChecking.
	t.Run("changed host key in direct ssh and mediated git", func(t *testing.T) {
		_, wrongHostPriv, err := ed25519.GenerateKey(rand.Reader)
		testutil.FailErr(t, "wrong host key", err)
		wrongHostSigner, err := ssh.NewSignerFromKey(wrongHostPriv)
		testutil.FailErr(t, "wrong host signer", err)
		_, conflictKnown, _, _ := setupIsolatedSSHHome(t, sshFixture.port, wrongHostSigner.PublicKey())

		conflictSSHCmd := fmt.Sprintf("SSH_AUTH_SOCK=%q ssh -F /dev/null -o UserKnownHostsFile=%q -p %d git@127.0.0.1 echo remote-ok", sockRef, conflictKnown, sshFixture.port)
		directSess := p.programWithArgs(t, map[string]any{
			"command": conflictSSHCmd,
			"capability_request": map[string]any{
				"loopback_connect": map[string]any{"ports": []any{int(sshFixture.port)}},
				"read_path":        []any{conflictKnown},
				"socket_paths":     []any{agentSock},
			},
		}, wire.CreateSessionRequest{}, "")
		grantSSHAuthority(t, directSess, agentSock, sshFixture.port)
		promptApprovingRecommended(t, directSess, "run the program")

		directRaw := rawToolResult(t, p.h, directSess.session, "program-1")
		for _, want := range []string{"SSH_HOST_KEY_CHANGED", conflictKnown, "StrictHostKeyChecking"} {
			if !strings.Contains(directRaw, want) {
				t.Fatalf("direct ssh changed key missing %q in:\n%s", want, directRaw)
			}
		}
		for _, forbidden := range []string{"SANDBOX_TRY_READ_PATH", "SANDBOX_TRY_SOCKET_PATH", "SANDBOX_TRY_CAPABILITIES"} {
			if strings.Contains(directRaw, forbidden) {
				t.Fatalf("direct ssh changed key emitted %s:\n%s", forbidden, directRaw)
			}
		}

		// Mediated native git: accept initial server host key into project known_hosts, then rotate server key.
		rotPort, activeSigner := startRotatingSSHGitServer(t, bareDir, userPub, wrongHostSigner)
		_, nextHostPriv, err := ed25519.GenerateKey(rand.Reader)
		testutil.FailErr(t, "next host key", err)
		nextHostSigner, err := ssh.NewSignerFromKey(nextHostPriv)
		testutil.FailErr(t, "next host signer", err)

		gitDir := newSeededGitWorkDir(t)
		gittest.Run(t, gitDir, "remote", "add", "origin", fmt.Sprintf("ssh://git@127.0.0.1:%d/repo.git", rotPort))
		gitMock := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{
			{
				Pattern: "seed known_hosts",
				ToolCalls: []llm.MockToolCall{{
					ID:   "git-seed-1",
					Name: "command",
					Args: map[string]any{
						"command":            "git ls-remote origin",
						"capability_request": map[string]any{"socket_paths": []any{agentSock}},
					},
				}},
				FollowUpText: "Seeded.",
			},
			{
				Pattern: "observe changed key",
				ToolCalls: []llm.MockToolCall{{
					ID:   "git-rotated-2",
					Name: "command",
					Args: map[string]any{
						"command":            "git ls-remote origin",
						"capability_request": map[string]any{"socket_paths": []any{agentSock}},
					},
				}},
				FollowUpText: "Detected rotation.",
			},
		}})
		gitSess := p.withClientAt(t, gitDir, gitMock, wire.CreateSessionRequest{}, "")
		gitSess.h.SeedProgress(t, t.Context(), gitSess.session)
		grantSSHAuthority(t, gitSess, agentSock, rotPort)
		promptApprovingRecommended(t, gitSess, "seed known_hosts")
		if seedOut := gitSess.outcome(t, "git-seed-1"); seedOut.Status != 0 {
			t.Fatalf("initial mediated git ls-remote failed: %+v", seedOut)
		}

		activeSigner.Store(&nextHostSigner)
		promptApprovingRecommended(t, gitSess, "observe changed key")

		gitRaw := rawToolResult(t, p.h, gitSess.session, "git-rotated-2")
		if !strings.Contains(gitRaw, "SSH_HOST_KEY_CHANGED") || strings.Contains(gitRaw, "SANDBOX_TRY_SOCKET_PATH") {
			t.Fatalf("mediated git changed key unexpected result:\n%s", gitRaw)
		}
	})

	// 6. Remote publickey rejection with available signing agent + mediated git missing agent / absent agent.
	t.Run("remote auth rejection and mediated git agent states", func(t *testing.T) {
		wrongAgentSock, _ := startSigningSSHAgent(t)
		t.Setenv("SSH_AUTH_SOCK", wrongAgentSock)
		_, trustedKnown, _, _ := setupIsolatedSSHHome(t, sshFixture.port, sshFixture.hostPub)
		wrongRef, _ := confine.SocketEndpointReferences(wrongAgentSock)

		// 6a. Direct ssh with wrong agent key -> SSH_REMOTE_AUTH_REJECTED.
		rejDirect := p.programWithArgs(t, map[string]any{
			"command": fmt.Sprintf("SSH_AUTH_SOCK=%q ssh -F /dev/null -o UserKnownHostsFile=%q -p %d git@127.0.0.1 echo remote-ok", wrongRef, trustedKnown, sshFixture.port),
			"capability_request": map[string]any{
				"loopback_connect": map[string]any{"ports": []any{int(sshFixture.port)}},
				"read_path":        []any{trustedKnown},
				"socket_paths":     []any{wrongAgentSock},
			},
		}, wire.CreateSessionRequest{}, "")
		grantSSHAuthority(t, rejDirect, wrongAgentSock, sshFixture.port)
		promptApprovingRecommended(t, rejDirect, "run the program")

		rejDirectRaw := rawToolResult(t, p.h, rejDirect.session, "program-1")
		if !strings.Contains(rejDirectRaw, "SSH_REMOTE_AUTH_REJECTED") || strings.Contains(rejDirectRaw, "SANDBOX_TRY_SOCKET_PATH") || strings.Contains(rejDirectRaw, "SANDBOX_TRY_READ_PATH") {
			t.Fatalf("direct ssh remote rejection unexpected result:\n%s", rejDirectRaw)
		}

		// 6b. Mediated git with wrong agent key -> SSH_REMOTE_AUTH_REJECTED.
		gitDir := newSeededGitWorkDir(t)
		gittest.Run(t, gitDir, "remote", "add", "origin", fmt.Sprintf("ssh://git@127.0.0.1:%d/repo.git", sshFixture.port))
		rejGit := p.withClientAt(t, gitDir, programModel(map[string]any{
			"command":            "git ls-remote origin",
			"capability_request": map[string]any{"socket_paths": []any{wrongAgentSock}},
		}), wire.CreateSessionRequest{}, "")
		rejGit.h.SeedProgress(t, t.Context(), rejGit.session)
		grantSSHAuthority(t, rejGit, wrongAgentSock, sshFixture.port)
		promptApprovingRecommended(t, rejGit, "run the program")

		rejGitRaw := rawToolResult(t, p.h, rejGit.session, "program-1")
		if !strings.Contains(rejGitRaw, "SSH_REMOTE_AUTH_REJECTED") || strings.Contains(rejGitRaw, "SANDBOX_TRY_SOCKET_PATH") {
			t.Fatalf("mediated git remote rejection unexpected result:\n%s", rejGitRaw)
		}

		// 6c. Mediated git without socket grant when host agent exists -> SANDBOX_TRY_SOCKET_PATH.
		t.Setenv("SSH_AUTH_SOCK", agentSock)
		needAgentGit := p.withClientAt(t, gitDir, programModel(map[string]any{
			"command": "git ls-remote origin",
		}), wire.CreateSessionRequest{}, "")
		needAgentGit.h.SeedProgress(t, t.Context(), needAgentGit.session)
		grantLoopbackPortsOnly(t, needAgentGit, sshFixture.port)
		promptApprovingRecommended(t, needAgentGit, "run the program")

		needAgentRaw := rawToolResult(t, p.h, needAgentGit.session, "program-1")
		for _, want := range []string{"SANDBOX_TRY_SOCKET_PATH", agentSock, "binds `SSH_AUTH_SOCK` automatically for native `git`"} {
			if !strings.Contains(needAgentRaw, want) {
				t.Fatalf("mediated git missing agent lacked %q in:\n%s", want, needAgentRaw)
			}
		}

		// 6d. Mediated git when host SSH_AUTH_SOCK is absent -> SSH_AGENT_UNAVAILABLE.
		t.Setenv("SSH_AUTH_SOCK", "")
		t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/nonexistent")
		noAgentGit := p.withClientAt(t, gitDir, programModel(map[string]any{
			"command": "git ls-remote origin",
		}), wire.CreateSessionRequest{}, "")
		noAgentGit.h.SeedProgress(t, t.Context(), noAgentGit.session)
		grantLoopbackPortsOnly(t, noAgentGit, sshFixture.port)
		promptApprovingRecommended(t, noAgentGit, "run the program")

		noAgentRaw := rawToolResult(t, p.h, noAgentGit.session, "program-1")
		if !strings.Contains(noAgentRaw, "SSH_AGENT_UNAVAILABLE") || strings.Contains(noAgentRaw, "SANDBOX_TRY_SOCKET_PATH") {
			t.Fatalf("mediated git absent agent unexpected result:\n%s", noAgentRaw)
		}

		// Verify socketref import remains live for helper parity.
		_ = socketref.Capture
	})
}

func TestMixedGitFetchAndGHAuthCredentialStoreRecovery(t *testing.T) {
	confinefixture.RequireRefusalReports(t)
	installBundledKeyMaterialFloor(t)
	rawHome, err := os.MkdirTemp("/tmp", "pw-gh-") //nolint:usetesting // Keep macOS Keychains path under Seatbelt kernel report size limit.
	testutil.FailErr(t, "mkdir gh home", err)
	t.Cleanup(func() { _ = os.RemoveAll(rawHome) })
	home := fspath.CanonicalPath(rawHome)
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	var wantRefusedStorePath string
	switch runtime.GOOS {
	case "darwin":
		keychainDir := filepath.Join(home, "Library", "Keychains")
		testutil.FailErr(t, "mkdir Keychains", os.MkdirAll(keychainDir, 0o700))
		wantRefusedStorePath = filepath.Join(keychainDir, "login.keychain-db")
		testutil.FailErr(t, "write fixture keychain", os.WriteFile(wantRefusedStorePath, []byte("gho_expired_or_rejected_fixture\n"), 0o600))
	case "linux":
		shortDir, err := os.MkdirTemp("/tmp", "pw-ghbus-") //nolint:usetesting // Unix socket path length limit.
		testutil.FailErr(t, "mkdir bus dir", err)
		t.Cleanup(func() { _ = os.RemoveAll(shortDir) })
		shortDir = fspath.CanonicalPath(shortDir)
		busSock := filepath.Join(shortDir, "bus")
		wantRefusedStorePath = fmt.Sprintf("/run/user/%d/bus", os.Getuid())
		dbusBin, err := exec.LookPath("dbus-daemon")
		testutil.FailErr(t, "locate dbus-daemon", err)
		daemon := exec.CommandContext(t.Context(), dbusBin, "--session", "--nofork", "--nopidfile", "--address=unix:path="+busSock, "--print-address=1")
		stdout, err := daemon.StdoutPipe()
		testutil.FailErr(t, "dbus stdout pipe", err)
		testutil.FailErr(t, "start fixture dbus-daemon", daemon.Start())
		t.Cleanup(func() { _ = daemon.Process.Kill(); _ = daemon.Wait() })
		_, err = bufio.NewReader(stdout).ReadString('\n')
		testutil.FailErr(t, "wait for fixture dbus-daemon", err)
		bus, err := dbus.Connect("unix:path="+busSock, dbus.WithContext(t.Context()))
		testutil.FailErr(t, "connect fixture dbus-daemon", err)
		t.Cleanup(func() { _ = bus.Close() })
		svc := &ghFixtureSecretService{}
		for pth, iface := range map[dbus.ObjectPath]string{
			"/org/freedesktop/secrets":                      "org.freedesktop.Secret.Service",
			"/org/freedesktop/secrets/collection/test/item": "org.freedesktop.Secret.Item",
			"/org/freedesktop/secrets/session/test":         "org.freedesktop.Secret.Session",
		} {
			testutil.FailErr(t, "export secret service", bus.Export(svc, pth, iface))
		}
		_, err = bus.RequestName("org.freedesktop.secrets", dbus.NameFlagDoNotQueue)
		testutil.FailErr(t, "request org.freedesktop.secrets", err)
		t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path="+busSock)
	default:
		t.Fatalf("unsupported platform %s", runtime.GOOS)
	}

	// API fixture rejecting the stored token with HTTP 401 when credential_store IS granted.
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
	}))
	t.Cleanup(apiServer.Close)
	parsedAPI, err := url.Parse(apiServer.URL)
	testutil.FailErr(t, "parse api url", err)
	apiPort, err := strconv.ParseUint(parsedAPI.Port(), 10, 16)
	testutil.FailErr(t, "parse api port", err)
	ghCfgDir := filepath.Join(home, ".config", "gh")
	testutil.FailErr(t, "mkdir gh config", os.MkdirAll(ghCfgDir, 0o700))
	testutil.FailErr(t, "write gh hosts.yml", os.WriteFile(filepath.Join(ghCfgDir, "hosts.yml"), []byte("api_url: "+apiServer.URL+"/user\n"), 0o600))

	p := newProgramHost(t)
	admissiontest.Enable(t)
	t.Setenv("LYCAON_SANDBOX", "on")
	gittest.UserCredentials(t, "alice", "remote-secret")

	// Install re-exec `gh` shim first on the engine's resolved PATH so confined `gh auth status` runs inside the sandbox.
	selfExe, err := os.Executable()
	testutil.FailErr(t, "os.Executable", err)
	rawBinDir, err := os.MkdirTemp("/tmp", "pw-ghbin-") //nolint:usetesting // Readable canonical PATH entry inside Seatbelt and Linux namespaces.
	testutil.FailErr(t, "mkdir gh bin", err)
	t.Cleanup(func() { _ = os.RemoveAll(rawBinDir) })
	testutil.FailErr(t, "chmod gh bin", os.Chmod(rawBinDir, 0o755))
	binDir := fspath.CanonicalPath(rawBinDir)
	// On Darwin, macOS Sandbox.kext suppresses violation reports from the same Mach-O vnode that called
	// sandbox_init(); reading the keychain via /bin/cat ensures a distinct vnode triggers kernel reporting.
	shimBody := "#!/bin/sh\nif [ \"$(uname -s)\" = \"Darwin\" ]; then\n  /bin/cat \"$HOME/Library/Keychains/login.keychain-db\" >/dev/null 2>&1 || {\n    echo \"You are not logged into any GitHub hosts. To log in, run: gh auth login\" >&2\n    exit 1\n  }\nfi\nexec '" + selfExe + "' " + ghFixtureVerb + " \"$@\"\n"
	testutil.FailErr(t, "write gh shim", os.WriteFile(filepath.Join(binDir, "gh"), []byte(shimBody), 0o755))

	// Seed authenticated HTTP Git remote for the sibling `git fetch` stage.
	httpSeed := t.TempDir()
	testutil.FailErr(t, "write http seed", os.WriteFile(filepath.Join(httpSeed, "README.md"), []byte("fetched\n"), 0o644))
	gittest.InitCommit(t, httpSeed, "http-seed")
	gittest.Run(t, httpSeed, "branch", "-M", "main")
	seedOID := strings.TrimSpace(gittest.Run(t, httpSeed, "rev-parse", "HEAD"))
	gitHTTP, _ := startAuthenticatedGitHTTPServer(t, httpSeed)
	parsedGit, err := url.Parse(gitHTTP.URL)
	testutil.FailErr(t, "parse git http url", err)
	gitPort, err := strconv.ParseUint(parsedGit.Port(), 10, 16)
	testutil.FailErr(t, "parse git port", err)

	workDir := newSeededGitWorkDir(t)
	fetchStage := fmt.Sprintf("git fetch %s/repo.git main", gitHTTP.URL)
	mixedCmd := fetchStage + " && gh auth status"

	mock := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{
		{
			Pattern: "run the program",
			ToolCalls: []llm.MockToolCall{{
				ID:   "mixed-1",
				Name: "command",
				Args: map[string]any{"command": mixedCmd},
			}},
			FollowUpText: "Fetch succeeded, gh blocked by credential store.",
		},
		{
			Pattern: "check gh with credential store",
			ToolCalls: []llm.MockToolCall{{
				ID:   "gh-only-2",
				Name: "command",
				Args: map[string]any{
					"command":            "gh auth status",
					"capability_request": map[string]any{"credential_store": true},
				},
			}},
			FollowUpText: "API rejected stored token.",
		},
	}})
	sess := p.withClientAt(t, workDir, mock, wire.CreateSessionRequest{}, "")
	basePath := lycexec.EffectivePathValue()
	lycexec.SetResolvedPathSource(func() string { return binDir + string(os.PathListSeparator) + basePath })
	t.Cleanup(func() { lycexec.SetResolvedPathSource(nil) })
	sess.h.SeedProgress(t, t.Context(), sess.session)
	grantLoopbackPortsOnly(t, sess, uint16(gitPort), uint16(apiPort))

	// 1. Without credential_store: `git fetch` succeeds and keeps FETCH_HEAD while `gh auth status`
	// fails on the OS credential store; token validity outside the sandbox is explicitly undetermined,
	// and only `gh auth status` remains outstanding.
	promptApprovingRecommended(t, sess, "run the program")

	gotFetchHead := strings.TrimSpace(gittest.Run(t, workDir, "rev-parse", "FETCH_HEAD"))
	if gotFetchHead != seedOID {
		t.Fatalf("FETCH_HEAD = %q, want %q", gotFetchHead, seedOID)
	}

	raw1 := rawToolResult(t, p.h, sess.session, "mixed-1")
	out1 := decodeProgramOutcomeFromToolResult(t, raw1)
	if out1.Status != 1 || !strings.Contains(out1.Stderr, "not logged into any GitHub hosts") {
		t.Fatalf("mixed outcome = status %d stderr %q", out1.Status, out1.Stderr)
	}
	for _, want := range []string{
		"SANDBOX_TRY_CREDENTIAL_STORE",
		wantRefusedStorePath,
		"gh auth status",
		"token validity outside the sandbox is undetermined",
		"`capability_request.credential_store: true`",
		"FETCH_HEAD",
	} {
		if !strings.Contains(raw1, want) {
			t.Fatalf("mixed fetch+gh result missing %q in:\n%s", want, raw1)
		}
	}

	// 2. With credential_store granted on ONLY the remaining `gh auth status` operation:
	// store access succeeds and the observed HTTP 401 API rejection is reported without re-offering credential_store.
	promptApprovingRecommended(t, sess, "check gh with credential store")

	raw2 := rawToolResult(t, p.h, sess.session, "gh-only-2")
	out2 := decodeProgramOutcomeFromToolResult(t, raw2)
	if out2.Status != 1 || !strings.Contains(out2.Stderr, "HTTP 401: Bad credentials") {
		t.Fatalf("granted gh outcome = status %d stderr %q", out2.Status, out2.Stderr)
	}
	if strings.Contains(raw2, "SANDBOX_TRY_CREDENTIAL_STORE") || strings.Contains(raw2, "token validity outside the sandbox is undetermined") {
		t.Fatalf("granted gh call falsely blamed credential store:\n%s", raw2)
	}
}
