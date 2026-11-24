package command

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

type directSSHStageFixture struct {
	home           string
	project        string
	knownHosts     string
	sshConfig      string
	privKey        string
	agentSock      string
	portNum        int
	cmdLine        string
	argv           []string
	env            []string
	baseBoundary   confine.Boundary
	serverSigner   ssh.Signer
	oldSigner      ssh.Signer
	writeKnownLine func(ssh.PublicKey)
}

func newDirectSSHStageFixture(t *testing.T) directSSHStageFixture {
	t.Helper()
	home := fspath.CanonicalPath(t.TempDir())
	t.Setenv("HOME", home)
	t.Setenv("LYCAON_CONFIG_DIR", filepath.Join(home, ".config", "paintedwolf"))
	prevKeys := confine.SetKeyMaterialPathsSource(func() []string { return []string{"~/.ssh/"} })
	t.Cleanup(func() { confine.SetKeyMaterialPathsSource(prevKeys) })
	sshDir := filepath.Join(home, ".ssh")
	testutil.FailErr(t, "mkdir .ssh", os.MkdirAll(sshDir, 0o700))

	_, serverPriv, err := ed25519.GenerateKey(rand.Reader)
	testutil.FailErr(t, "server key", err)
	serverSigner, err := ssh.NewSignerFromKey(serverPriv)
	testutil.FailErr(t, "server signer", err)

	_, oldPriv, err := ed25519.GenerateKey(rand.Reader)
	testutil.FailErr(t, "old key", err)
	oldSigner, err := ssh.NewSignerFromKey(oldPriv)
	testutil.FailErr(t, "old signer", err)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	testutil.FailErr(t, "listen ssh", err)
	t.Cleanup(func() { _ = ln.Close() })
	_, portStr, err := net.SplitHostPort(ln.Addr().String())
	testutil.FailErr(t, "split port", err)
	portNum, err := strconv.Atoi(portStr)
	testutil.FailErr(t, "atoi port", err)

	srvCfg := &ssh.ServerConfig{
		PublicKeyCallback: func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) {
			return nil, fmt.Errorf("rejected")
		},
	}
	srvCfg.AddHostKey(serverSigner)
	go func() {
		for {
			c, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			go func(raw net.Conn) {
				defer func() { _ = raw.Close() }()
				_, _, _, _ = ssh.NewServerConn(raw, srvCfg)
			}(c)
		}
	}()

	knownHosts := filepath.Join(sshDir, "known_hosts")
	sshConfig := filepath.Join(sshDir, "config")
	privKey := filepath.Join(sshDir, "id_ed25519")
	testutil.FailErr(t, "write config", os.WriteFile(sshConfig, []byte("Host *\n"), 0o600))
	testutil.FailErr(t, "write key", os.WriteFile(privKey, []byte("PRIVATE KEY\n"), 0o600))

	agentDir, err := os.MkdirTemp("/tmp", "pwssh-") //nolint:usetesting // Unix socket path length limit on macOS.
	testutil.FailErr(t, "mkdir agent", err)
	t.Cleanup(func() { _ = os.RemoveAll(agentDir) })
	agentDir = fspath.CanonicalPath(agentDir)
	agentSock := filepath.Join(agentDir, "agent.sock")
	agentLn, err := net.Listen("unix", agentSock)
	testutil.FailErr(t, "listen agent", err)
	t.Cleanup(func() { _ = agentLn.Close() })
	t.Setenv("SSH_AUTH_SOCK", agentSock)

	writeKnownLine := func(pub ssh.PublicKey) {
		line := fmt.Sprintf("[127.0.0.1]:%d %s", portNum, ssh.MarshalAuthorizedKey(pub))
		testutil.FailErr(t, "write known_hosts", os.WriteFile(knownHosts, []byte(line), 0o600))
	}
	writeKnownLine(serverSigner.PublicKey())

	project := t.TempDir()
	baseConf := confine.Confinement{Roots: []string{project}}
	return directSSHStageFixture{
		home:           home,
		project:        project,
		knownHosts:     knownHosts,
		sshConfig:      sshConfig,
		privKey:        privKey,
		agentSock:      agentSock,
		portNum:        portNum,
		cmdLine:        fmt.Sprintf("ssh -p %d git@127.0.0.1 echo ok", portNum),
		argv:           []string{"ssh", "-p", portStr, "git@127.0.0.1", "echo", "ok"},
		env:            []string{"HOME=" + home, "SSH_AUTH_SOCK=" + agentSock},
		baseBoundary:   confine.BoundaryOf(&baseConf),
		serverSigner:   serverSigner,
		oldSigner:      oldSigner,
		writeKnownLine: writeKnownLine,
	}
}

func TestEvaluateDirectSSHStageClassificationAndProbeFiltering(t *testing.T) {
	f := newDirectSSHStageFixture(t)

	t.Run("broker refusal suppresses ssh speculation", func(t *testing.T) {
		rawRefusals := confine.SandboxRefusals{
			Witness: confine.WitnessKernel,
			Refusals: []confine.SandboxRefusal{
				{Operation: "file-read-data", Target: f.knownHosts, Recovery: confine.RecoverReadPath, Grant: f.knownHosts},
				{Operation: "file-read-data", Target: f.privKey, Recovery: confine.RecoverReadPath, Grant: f.privKey},
				{Operation: "network-outbound", Target: f.agentSock, Recovery: confine.RecoverSocketPath, Grant: f.agentSock},
			},
		}
		filtered, obs := evaluateDirectSSHStage(
			t.Context(), tools.ToolContext{}, f.baseBoundary, f.cmdLine, f.argv, f.env, 255,
			[]byte("ssh_exchange_identification: Connection closed\n"),
			[]confine.EgressHost{{Host: "127.0.0.1", Port: uint16(f.portNum), Allowed: false}},
			rawRefusals,
		)
		if len(obs) != 0 || len(filtered.Refusals) != 0 {
			t.Fatalf("broker refusal leaked SSH recovery: obs=%+v refusals=%+v", obs, filtered.Refusals)
		}
	})

	t.Run("incomplete witness still recovers single known_hosts and agent socket", func(t *testing.T) {
		filtered, obs := evaluateDirectSSHStage(
			t.Context(), tools.ToolContext{}, f.baseBoundary, f.cmdLine, f.argv, f.env, 255,
			[]byte("Host key verification failed.\n"),
			nil,
			confine.SandboxRefusals{Witness: confine.WitnessIncomplete},
		)
		if len(obs) != 1 || obs[0].Cause != confine.SSHFailureHostKeyUnreadable || obs[0].TrustPath != f.knownHosts || obs[0].AgentSocket != f.agentSock {
			t.Fatalf("unexpected SSH observation: %+v", obs)
		}
		if !slices.Contains(obs[0].ProbedPaths, f.privKey) || !slices.Contains(obs[0].ProbedPaths, f.sshConfig) {
			t.Fatalf("expected config and private key in ProbedPaths, got %+v", obs[0].ProbedPaths)
		}
		if len(filtered.Refusals) != 2 {
			t.Fatalf("expected exactly known_hosts + agent socket refusals, got %+v", filtered.Refusals)
		}
		for _, r := range filtered.Refusals {
			if r.Grant == f.privKey || r.Grant == f.sshConfig {
				t.Fatalf("speculative probe leaked into actionable refusals: %+v", r)
			}
		}
	})

	t.Run("changed host key", func(t *testing.T) {
		f.writeKnownLine(f.oldSigner.PublicKey())
		t.Cleanup(func() { f.writeKnownLine(f.serverSigner.PublicKey()) })

		confWithTrust := confine.Confinement{
			Roots:               []string{f.project},
			ProtectedReadGrants: []confine.ProtectedPathGrant{confine.NewProtectedPathGrant(f.knownHosts)},
		}
		filtered, obs := evaluateDirectSSHStage(
			t.Context(), tools.ToolContext{}, confine.BoundaryOf(&confWithTrust), f.cmdLine, f.argv, f.env, 255,
			[]byte("WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!\nHost key verification failed.\n"),
			nil,
			confine.SandboxRefusals{
				Witness: confine.WitnessKernel,
				Refusals: []confine.SandboxRefusal{
					{Operation: "file-read-data", Target: f.privKey, Recovery: confine.RecoverReadPath, Grant: f.privKey},
				},
			},
		)
		if len(obs) != 1 || obs[0].Cause != confine.SSHFailureHostKeyChanged || obs[0].TrustPath != f.knownHosts {
			t.Fatalf("expected host_key_changed on %s, got %+v", f.knownHosts, obs)
		}
		if len(filtered.Refusals) != 0 {
			t.Fatalf("expected zero capability refusals on host_key_changed, got %+v", filtered.Refusals)
		}
	})

	t.Run("bound agent remote rejection", func(t *testing.T) {
		confWithTrust := confine.Confinement{
			Roots:               []string{f.project},
			ProtectedReadGrants: []confine.ProtectedPathGrant{confine.NewProtectedPathGrant(f.knownHosts)},
		}
		sockToken := "{{paintedwolf-socket:" + url.PathEscape(f.agentSock) + "}}"
		tctx := tools.ToolContext{
			Socket: tools.InvocationSocket{
				SocketGrants: []confine.SocketGrant{{ApprovedPath: f.agentSock, ResolvedPath: fspath.CanonicalPath(f.agentSock)}},
			},
		}
		boundEnv := []string{"HOME=" + f.home, "SSH_AUTH_SOCK=" + sockToken}
		filtered, obs := evaluateDirectSSHStage(
			t.Context(), tctx, confine.BoundaryOf(&confWithTrust), f.cmdLine, f.argv, boundEnv, 255,
			[]byte("git@127.0.0.1: Permission denied (publickey).\n"),
			nil,
			confine.SandboxRefusals{
				Witness: confine.WitnessKernel,
				Refusals: []confine.SandboxRefusal{
					{Operation: "file-read-data", Target: f.privKey, Recovery: confine.RecoverReadPath, Grant: f.privKey},
				},
			},
		)
		if len(obs) != 1 || obs[0].Cause != confine.SSHFailurePublicKeyRejected || obs[0].AgentSocket != f.agentSock {
			t.Fatalf("expected publickey_rejected with bound agent, got %+v", obs)
		}
		if len(filtered.Refusals) != 0 {
			t.Fatalf("expected zero capability refusals on publickey_rejected, got %+v", filtered.Refusals)
		}
	})

	t.Run("system resolver socket ignored before agent socket", func(t *testing.T) {
		confWithTrust := confine.Confinement{
			Roots:               []string{f.project},
			ProtectedReadGrants: []confine.ProtectedPathGrant{confine.NewProtectedPathGrant(f.knownHosts)},
		}
		filtered, obs := evaluateDirectSSHStage(
			t.Context(), tools.ToolContext{}, confine.BoundaryOf(&confWithTrust), f.cmdLine, f.argv, f.env, 255,
			[]byte("git@127.0.0.1: Permission denied (publickey).\n"),
			nil,
			confine.SandboxRefusals{
				Witness: confine.WitnessKernel,
				Refusals: []confine.SandboxRefusal{
					{Operation: "network-outbound", Target: "/run/nscd/socket", Recovery: confine.RecoverSocketPath, Grant: "/run/nscd/socket"},
					{Operation: "network-outbound", Target: f.agentSock, Recovery: confine.RecoverSocketPath, Grant: f.agentSock},
				},
			},
		)
		if len(obs) != 1 || obs[0].Cause != confine.SSHFailureAgentRequired || obs[0].AgentSocket != f.agentSock {
			t.Fatalf("expected agent_required for %s, got %+v", f.agentSock, obs)
		}
		if len(filtered.Refusals) != 1 || filtered.Refusals[0].Grant != f.agentSock {
			t.Fatalf("expected only %s in filtered refusals, got %+v", f.agentSock, filtered.Refusals)
		}
		if slices.Contains(obs[0].ProbedPaths, "/run/nscd/socket") {
			t.Fatalf("unexpected system socket in ProbedPaths: %+v", obs[0].ProbedPaths)
		}
	})
}
