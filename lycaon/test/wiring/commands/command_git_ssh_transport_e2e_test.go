//go:build unix

package commands

import (
	"context"
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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	sshagent "golang.org/x/crypto/ssh/agent"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/confine/admissiontest"
	"github.com/lycaon/lycaon/internal/egressproxy"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/gitengine"
	"github.com/lycaon/lycaon/internal/gitexec"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/socketref"
	"github.com/lycaon/lycaon/internal/sshproxy"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type sshGitFixture struct {
	addr          string
	port          uint16
	hostPub       ssh.PublicKey
	acceptedConns atomic.Int32
	stallSession  atomic.Bool
	stallEntered  chan struct{}
}

func startSSHGitServer(t *testing.T, repoDir string, userPub ssh.PublicKey) *sshGitFixture {
	t.Helper()
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Fatalf("system ssh is required: %v", err)
	}
	gitBin, err := gitengine.BinaryPath()
	testutil.FailErr(t, "resolve bundled git", err)

	_, hostPriv, err := ed25519.GenerateKey(rand.Reader)
	testutil.FailErr(t, "generate host key", err)
	hostSigner, err := ssh.NewSignerFromKey(hostPriv)
	testutil.FailErr(t, "host signer", err)

	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if string(key.Marshal()) == string(userPub.Marshal()) {
				return &ssh.Permissions{}, nil
			}
			return nil, fmt.Errorf("unauthorized key")
		},
	}
	cfg.AddHostKey(hostSigner)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	testutil.FailErr(t, "listen ssh server", err)
	t.Cleanup(func() { _ = ln.Close() })

	_, portStr, err := net.SplitHostPort(ln.Addr().String())
	testutil.FailErr(t, "parse ssh port", err)
	portNum, err := strconv.ParseUint(portStr, 10, 16)
	testutil.FailErr(t, "parse uint16 port", err)

	fixture := &sshGitFixture{
		addr:         ln.Addr().String(),
		port:         uint16(portNum),
		hostPub:      hostSigner.PublicKey(),
		stallEntered: make(chan struct{}, 4),
	}
	go func() {
		for {
			rawConn, err := ln.Accept()
			if err != nil {
				return
			}
			fixture.acceptedConns.Add(1)
			go serveSSHGitConn(rawConn, cfg, gitBin, repoDir, fixture)
		}
	}()
	return fixture
}

func serveSSHGitConn(rawConn net.Conn, cfg *ssh.ServerConfig, gitBin, repoDir string, fixture *sshGitFixture) {
	defer func() { _ = rawConn.Close() }()
	if fixture.stallSession.Load() {
		select {
		case fixture.stallEntered <- struct{}{}:
		default:
		}
		buf := make([]byte, 64)
		for {
			if _, err := rawConn.Read(buf); err != nil {
				return
			}
		}
	}
	sshConn, chans, reqs, err := ssh.NewServerConn(rawConn, cfg)
	if err != nil {
		return
	}
	defer func() { _ = sshConn.Close() }()
	go ssh.DiscardRequests(reqs)

	for newChan := range chans {
		if newChan.ChannelType() != "session" {
			_ = newChan.Reject(ssh.UnknownChannelType, "unsupported channel")
			continue
		}
		ch, chReqs, err := newChan.Accept()
		if err != nil {
			continue
		}
		go handleSSHGitSession(ch, chReqs, gitBin, repoDir)
	}
}

func handleSSHGitSession(ch ssh.Channel, reqs <-chan *ssh.Request, gitBin, repoDir string) {
	defer func() { _ = ch.Close() }()
	var gitProtocol string
	for req := range reqs {
		switch req.Type {
		case "env":
			var envReq struct{ Name, Value string }
			if ssh.Unmarshal(req.Payload, &envReq) == nil && envReq.Name == "GIT_PROTOCOL" {
				gitProtocol = envReq.Value
			}
			if req.WantReply {
				_ = req.Reply(true, nil)
			}
		case "exec":
			var execReq struct{ Command string }
			if err := ssh.Unmarshal(req.Payload, &execReq); err != nil {
				if req.WantReply {
					_ = req.Reply(false, nil)
				}
				return
			}
			var sub string
			switch {
			case strings.Contains(execReq.Command, "upload-pack"):
				sub = "upload-pack"
			case strings.Contains(execReq.Command, "receive-pack"):
				sub = "receive-pack"
			default:
				if req.WantReply {
					_ = req.Reply(false, nil)
				}
				return
			}
			if req.WantReply {
				_ = req.Reply(true, nil)
			}
			cmd := exec.Command(gitBin, sub, repoDir)
			env := append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
			if gitProtocol != "" {
				env = append(env, "GIT_PROTOCOL="+gitProtocol)
			}
			cmd.Env = env
			cmd.Stdin = ch
			cmd.Stdout = ch
			cmd.Stderr = ch.Stderr()
			exitCode := uint32(0)
			if runErr := cmd.Run(); runErr != nil {
				exitCode = 1
			}
			_ = ch.CloseWrite()
			_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{exitCode}))
			return
		default:
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
		}
	}
}

func startSigningSSHAgent(t *testing.T) (string, ssh.PublicKey) {
	t.Helper()
	_, userPriv, err := ed25519.GenerateKey(rand.Reader)
	testutil.FailErr(t, "generate user key", err)
	signer, err := ssh.NewSignerFromKey(userPriv)
	testutil.FailErr(t, "user signer", err)

	keyring := sshagent.NewKeyring()
	testutil.FailErr(t, "add key to agent", keyring.Add(sshagent.AddedKey{PrivateKey: userPriv}))

	dir, err := os.MkdirTemp("/tmp", "pwssh-") //nolint:usetesting // Unix socket path length limit.
	testutil.FailErr(t, "agent dir", err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sockPath := filepath.Join(dir, "agent.sock")
	ln, err := net.Listen("unix", sockPath)
	testutil.FailErr(t, "listen agent", err)
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				_ = sshagent.ServeAgent(keyring, conn)
			}()
		}
	}()
	return sockPath, signer.PublicKey()
}

func createBareSeededRepo(t *testing.T) (bareDir, seedWorkDir, headOID string) {
	t.Helper()
	seedWorkDir = t.TempDir()
	testutil.FailErr(t, "write seed file", os.WriteFile(filepath.Join(seedWorkDir, "README.md"), []byte("initial\n"), 0o644))
	gittest.InitCommit(t, seedWorkDir, "initial")
	gittest.Run(t, seedWorkDir, "branch", "-M", "main")
	headOID = strings.TrimSpace(gittest.Run(t, seedWorkDir, "rev-parse", "HEAD"))

	bareParent := t.TempDir()
	bareDir = filepath.Join(bareParent, "remote.git")
	gittest.Run(t, bareParent, "clone", "--bare", seedWorkDir, bareDir)
	gittest.Run(t, bareDir, "symbolic-ref", "HEAD", "refs/heads/main")
	return bareDir, seedWorkDir, headOID
}

func startAuthenticatedGitHTTPServer(t *testing.T, repo string) (*httptest.Server, func() bool) {
	t.Helper()
	bin, err := gitengine.BinaryPath()
	testutil.FailErr(t, "resolve bundled git", err)
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
		env := append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		if proto := r.Header.Get("Git-Protocol"); proto != "" {
			env = append(env, "GIT_PROTOCOL="+proto)
		}
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/info/refs"):
			w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
			pkt := "# service=git-upload-pack\n"
			_, _ = fmt.Fprintf(w, "%04x%s0000", len(pkt)+4, pkt)
			cmd := exec.Command(bin, "upload-pack", "--stateless-rpc", "--advertise-refs", repo)
			cmd.Env = env
			cmd.Stdout = w
			_ = cmd.Run()
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/git-upload-pack"):
			w.Header().Set("Content-Type", "application/x-git-upload-pack-result")
			cmd := exec.Command(bin, "upload-pack", "--stateless-rpc", repo)
			cmd.Env = env
			cmd.Stdin = r.Body
			cmd.Stdout = w
			_ = cmd.Run()
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return authenticated
	}
}

func grantSSHAuthority(t *testing.T, s *commandProgramSession, agentSock string, ports ...uint16) {
	t.Helper()
	if len(ports) > 0 {
		if !s.h.Server.Admin.Capabilities.Installation.Loopback.GrantChat(s.session, ports, "ssh-git-fixture", "test-ssh", nil) {
			t.Fatal("loopback grant failed")
		}
	}
	lease, err := socketref.Capture(agentSock)
	testutil.FailErr(t, "capture agent socket", err)
	t.Cleanup(lease.Close)
	grant := confine.SocketGrant{ApprovedPath: agentSock, ResolvedPath: fspath.CanonicalPath(agentSock), Binding: lease.Binding()}
	if !s.h.Server.Admin.Capabilities.Installation.Sockets.GrantChat(s.session, grant, "ssh-agent", "test-agent", "", nil) {
		t.Fatal("socket grant failed")
	}
}

func promptApprovingRecommended(t *testing.T, s *commandProgramSession, text string) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		_, err := s.h.Sessions.Manager.Submissions.Prompt(t.Context(), s.session, text)
		done <- err
	}()
	kind := wire.CheckpointKindToolApproval
	var runErr error
	testutil.WaitFor(t, "prompt completes answering approvals", func() bool {
		select {
		case runErr = <-done:
			return true
		default:
		}
		pending, err := s.h.Sessions.Checkpoints.ListPending(t.Context(), s.session, &kind)
		if err != nil || len(pending) == 0 {
			return false
		}
		for _, cp := range pending {
			_, err := s.h.Sessions.Checkpoints.(*hitl.Checkpoints).Authority.ResolveApprovalOption(
				s.h.OwnerCtx(t, t.Context()), s.session, cp.ID, cp.ToolApproval.Plan.RecommendedOptionID,
			)
			testutil.FailErr(t, "approve checkpoint", err)
		}
		return false
	})
	testutil.FailErr(t, "complete prompt", runErr)
}

func TestMediatedGitSSHTransfersWithRealGitAndOpenSSH(t *testing.T) {
	p := newProgramHost(t)
	admissiontest.Enable(t)
	t.Setenv("LYCAON_SANDBOX", "on")

	agentSock, userPub := startSigningSSHAgent(t)
	t.Setenv("SSH_AUTH_SOCK", agentSock)

	bareDir, seedWork, seedOID := createBareSeededRepo(t)
	sshFixture := startSSHGitServer(t, bareDir, userPub)
	remoteURL := fmt.Sprintf("ssh://git@127.0.0.1:%d/repo.git", sshFixture.port)

	// 1. Denied loopback port prevents any upstream TCP connection.
	denied := p.programWithArgs(t, map[string]any{
		"command":            "git ls-remote " + remoteURL,
		"capability_request": map[string]any{"socket_paths": []any{agentSock}},
	}, wire.CreateSessionRequest{}, "")
	gittest.InitCommit(t, denied.dir, "local")
	grantSSHAuthority(t, denied, agentSock)
	runCommandWithApprovals(t, denied)
	if got := denied.outcome(t, "program-1"); got.Status == 0 {
		t.Fatalf("ungranted loopback dial succeeded: %+v", got)
	}
	if sshFixture.acceptedConns.Load() != 0 {
		t.Fatalf("denied dial reached upstream %d times", sshFixture.acceptedConns.Load())
	}

	// 2. Allowed mediated ls-remote, clone, fetch, pull and push over real OpenSSH.
	testutil.FailErr(t, "advance seed", os.WriteFile(filepath.Join(seedWork, "next.txt"), []byte("advance\n"), 0o644))
	gittest.Run(t, seedWork, "add", "next.txt")
	gittest.Run(t, seedWork, "commit", "-m", "advance")
	nextOID := strings.TrimSpace(gittest.Run(t, seedWork, "rev-parse", "HEAD"))

	script1 := strings.Join([]string{
		"git ls-remote " + remoteURL + " refs/heads/main",
		"git clone " + remoteURL + " cloned",
	}, "\n")
	script2 := strings.Join([]string{
		"git -C cloned fetch scp-origin",
		"git -C cloned pull --ff-only origin main",
		"git -C cloned push origin HEAD:refs/heads/pushed-from-agent",
	}, "\n")
	mock := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{
		{
			Pattern:      "run the program",
			ToolCalls:    []llm.MockToolCall{{ID: "program-1", Name: "command", Args: map[string]any{"command": script1, "capability_request": map[string]any{"socket_paths": []any{agentSock}}}}},
			FollowUpText: "Cloned.",
		},
		{
			Pattern:      "pull and push",
			ToolCalls:    []llm.MockToolCall{{ID: "program-2", Name: "command", Args: map[string]any{"command": script2, "capability_request": map[string]any{"socket_paths": []any{agentSock}}}}},
			FollowUpText: "Pulled and pushed.",
		},
	}})
	allowed := p.withClient(t, mock, wire.CreateSessionRequest{}, "")
	allowed.h.SeedProgress(t, t.Context(), allowed.session)
	gittest.InitCommit(t, allowed.dir, "local")
	grantSSHAuthority(t, allowed, agentSock, sshFixture.port)
	runCommandWithApprovals(t, allowed)
	got := allowed.outcome(t, "program-1")
	if got.Status != 0 || !strings.Contains(got.Stdout, seedOID) {
		t.Fatalf("mediated ls-remote/clone failed: status=%d stdout=%q stderr=%q", got.Status, got.Stdout, got.Stderr)
	}
	clonedDir := filepath.Join(allowed.dir, "cloned")
	if clonedHead := strings.TrimSpace(gittest.Run(t, clonedDir, "rev-parse", "HEAD")); clonedHead != seedOID {
		t.Fatalf("cloned HEAD = %q, want %q", clonedHead, seedOID)
	}

	// Advance remote and exercise pull (fetch phase + fast-forward) and push.
	gittest.Run(t, seedWork, "push", bareDir, "HEAD:refs/heads/main")
	// Also configure url.*.insteadOf so SCP syntax resolves through Git's checked remote resolution.
	gittest.Run(t, clonedDir, "config", fmt.Sprintf("url.ssh://git@127.0.0.1:%d/.insteadOf", sshFixture.port), "git@ssh-scp.fixture:")
	gittest.Run(t, clonedDir, "remote", "add", "scp-origin", "git@ssh-scp.fixture:repo.git")

	promptApprovingRecommended(t, allowed, "pull and push")
	got2 := allowed.outcome(t, "program-2")
	if got2.Status != 0 {
		t.Fatalf("mediated fetch/pull/push failed: status=%d stdout=%q stderr=%q", got2.Status, got2.Stdout, got2.Stderr)
	}
	if pulledHead := strings.TrimSpace(gittest.Run(t, clonedDir, "rev-parse", "HEAD")); pulledHead != nextOID {
		t.Fatalf("pulled HEAD = %q, want %q", pulledHead, nextOID)
	}
	if pushedRef := strings.TrimSpace(gittest.Run(t, bareDir, "rev-parse", "refs/heads/pushed-from-agent")); pushedRef != nextOID {
		t.Fatalf("pushed remote ref = %q, want %q", pushedRef, nextOID)
	}
}

func TestMediatedGitMixedAllMultiRemotePartialSuccessAndStableEnv(t *testing.T) {
	p := newProgramHost(t)
	admissiontest.Enable(t)
	t.Setenv("LYCAON_SANDBOX", "on")
	gittest.UserCredentials(t, "alice", "remote-secret")

	agentSock, userPub := startSigningSSHAgent(t)
	t.Setenv("SSH_AUTH_SOCK", agentSock)

	sshBare, _, sshOID := createBareSeededRepo(t)
	sshGood := startSSHGitServer(t, sshBare, userPub)

	deniedBare, _, _ := createBareSeededRepo(t)
	sshDenied := startSSHGitServer(t, deniedBare, userPub)

	httpSeed := t.TempDir()
	testutil.FailErr(t, "write http seed", os.WriteFile(filepath.Join(httpSeed, "HTTP.md"), []byte("http\n"), 0o644))
	gittest.InitCommit(t, httpSeed, "http-seed")
	gittest.Run(t, httpSeed, "branch", "-M", "main")
	httpOID := strings.TrimSpace(gittest.Run(t, httpSeed, "rev-parse", "HEAD"))
	httpServer, authenticated := startAuthenticatedGitHTTPServer(t, httpSeed)
	parsedHTTP, err := url.Parse(httpServer.URL)
	testutil.FailErr(t, "parse http server", err)
	httpPort, err := strconv.ParseUint(parsedHTTP.Port(), 10, 16)
	testutil.FailErr(t, "parse http port", err)

	// Mixed fetch --all with both HTTP credential service and managed SSH service.
	mixed := p.programWithArgs(t, map[string]any{
		"command":            "git fetch --all",
		"capability_request": map[string]any{"socket_paths": []any{agentSock}},
	}, wire.CreateSessionRequest{}, "")
	gittest.InitCommit(t, mixed.dir, "local")
	gittest.Run(t, mixed.dir, "remote", "add", "http-rem", httpServer.URL+"/repo.git")
	gittest.Run(t, mixed.dir, "remote", "add", "ssh-rem", fmt.Sprintf("ssh://git@127.0.0.1:%d/repo.git", sshGood.port))
	grantSSHAuthority(t, mixed, agentSock, sshGood.port, uint16(httpPort))
	runCommandWithApprovals(t, mixed, "recommended")
	if got := mixed.outcome(t, "program-1"); got.Status != 0 {
		t.Fatalf("mixed fetch --all failed: %+v", got)
	}
	if !authenticated() {
		t.Fatal("mixed fetch --all did not authenticate HTTP remote")
	}
	if gotSSH := strings.TrimSpace(gittest.Run(t, mixed.dir, "rev-parse", "refs/remotes/ssh-rem/main")); gotSSH != sshOID {
		t.Fatalf("ssh-rem/main = %q, want %q", gotSSH, sshOID)
	}
	if gotHTTP := strings.TrimSpace(gittest.Run(t, mixed.dir, "rev-parse", "refs/remotes/http-rem/main")); gotHTTP != httpOID {
		t.Fatalf("http-rem/main = %q, want %q", gotHTTP, httpOID)
	}

	// Multi-remote partial success: sshGood succeeds and retains its ref while sshDenied gets zero upstream connections.
	partial := p.programWithArgs(t, map[string]any{
		"command":            "git fetch --all",
		"capability_request": map[string]any{"socket_paths": []any{agentSock}},
	}, wire.CreateSessionRequest{}, "")
	gittest.InitCommit(t, partial.dir, "partial")
	gittest.Run(t, partial.dir, "config", "fetch.parallel", "2")
	gittest.Run(t, partial.dir, "remote", "add", "good", fmt.Sprintf("ssh://git@127.0.0.1:%d/repo.git", sshGood.port))
	gittest.Run(t, partial.dir, "remote", "add", "blocked", fmt.Sprintf("ssh://git@127.0.0.1:%d/repo.git", sshDenied.port))
	grantSSHAuthority(t, partial, agentSock, sshGood.port)
	runCommandWithApprovals(t, partial)
	if got := partial.outcome(t, "program-1"); got.Status == 0 {
		t.Fatalf("partial fetch unexpectedly returned 0: %+v", got)
	}
	if sshDenied.acceptedConns.Load() != 0 {
		t.Fatalf("blocked remote accepted %d upstream connections", sshDenied.acceptedConns.Load())
	}
	if retained := strings.TrimSpace(gittest.Run(t, partial.dir, "rev-parse", "refs/remotes/good/main")); retained != sshOID {
		t.Fatalf("completed sibling remote ref lost: got %q, want %q", retained, sshOID)
	}

	// Stable general child environment: external subprocesses gain no per-launch LYCAON_GIT_SSH_SOCKET.
	envCheck1 := p.program(t, "env", "")
	runCommandWithApprovals(t, envCheck1)
	out1 := envCheck1.outcome(t, "program-1").Stdout
	if strings.Contains(out1, sshproxy.SocketEnv+"=") {
		t.Fatalf("general child environment leaked %s:\n%s", sshproxy.SocketEnv, out1)
	}
}

func countOpenFDs(t *testing.T) int {
	t.Helper()
	dir := "/proc/self/fd"
	if runtime.GOOS == "darwin" {
		dir = "/dev/fd"
	}
	entries, err := os.ReadDir(dir)
	testutil.FailErr(t, "read open descriptors", err)
	return len(entries)
}

func TestMediatedGitExpiredLeaseDirectIPLocalRetainedJobAndHandleCounts(t *testing.T) {
	p := newProgramHost(t)
	admissiontest.Enable(t)
	t.Setenv("LYCAON_SANDBOX", "on")

	agentSock, userPub := startSigningSSHAgent(t)
	t.Setenv("SSH_AUTH_SOCK", agentSock)

	bareDir, seedWork, seedOID := createBareSeededRepo(t)
	sshFixture := startSSHGitServer(t, bareDir, userPub)
	remoteURL := fmt.Sprintf("ssh://git@127.0.0.1:%d/repo.git", sshFixture.port)

	// 1. Expired lease dial reaches zero upstream connections.
	expiredBoundary := &confine.Confinement{
		Roots:           []string{t.TempDir()},
		Network:         confine.NetworkProxyOnly,
		ProjectID:       "proj-expired",
		LoopbackConnect: true,
	}
	expiredLease, err := confine.BindAction(expiredBoundary, confine.EgressCommand{
		SessionID: "s-expired", ActionID: "a-expired", ToolName: "git", Image: "git",
	})
	testutil.FailErr(t, "bind action", err)
	expiredLease.Close(context.Background())
	ep, err := egressproxy.ParseTCPEndpoint("127.0.0.1", sshFixture.port)
	testutil.FailErr(t, "parse endpoint", err)
	if res, err := expiredLease.DialManagedTCP(context.Background(), ep); err == nil || res.Conn != nil || res.Disposition != egressproxy.TCPDialLeaseEnded {
		t.Fatalf("DialManagedTCP after Close = (%+v, %v), want (TCPDialLeaseEnded, err)", res, err)
	}
	if sshFixture.acceptedConns.Load() != 0 {
		t.Fatalf("expired lease dial reached upstream %d times", sshFixture.acceptedConns.Load())
	}

	// 2. Local repository clone remains unchanged and touches zero network endpoints.
	localClone := p.program(t, "git clone ./seed local-copy", "")
	gittest.Run(t, localClone.dir, "clone", seedWork, filepath.Join(localClone.dir, "seed"))
	runCommandWithApprovals(t, localClone)
	if got := localClone.outcome(t, "program-1"); got.Status != 0 {
		t.Fatalf("local clone failed: %+v", got)
	}
	if gotLocal := strings.TrimSpace(gittest.Run(t, filepath.Join(localClone.dir, "local-copy"), "rev-parse", "HEAD")); gotLocal != seedOID {
		t.Fatalf("local clone HEAD = %q, want %q", gotLocal, seedOID)
	}

	// 3. Direct-IP Git network command bypasses proxy/helper mediation and records direct_ip authz rows.
	httpSeed := t.TempDir()
	testutil.FailErr(t, "write direct http seed", os.WriteFile(filepath.Join(httpSeed, "DIRECT.md"), []byte("direct\n"), 0o644))
	gittest.InitCommit(t, httpSeed, "direct-http")
	gittest.Run(t, httpSeed, "branch", "-M", "main")
	directHTTPOID := strings.TrimSpace(gittest.Run(t, httpSeed, "rev-parse", "HEAD"))
	httpServer, _ := startAuthenticatedGitHTTPServer(t, httpSeed)
	parsedDirectHTTP, err := url.Parse(httpServer.URL)
	testutil.FailErr(t, "parse direct http server", err)
	directHTTPPort, err := strconv.ParseUint(parsedDirectHTTP.Port(), 10, 16)
	testutil.FailErr(t, "parse direct http port", err)
	gittest.UserCredentials(t, "alice", "remote-secret")

	direct := p.programWithArgs(t, map[string]any{
		"command":            "git ls-remote " + httpServer.URL + "/repo.git refs/heads/main",
		"capability_request": map[string]any{"direct_ip": true},
	}, wire.CreateSessionRequest{}, "")
	gittest.InitCommit(t, direct.dir, "direct-init")
	for _, q := range direct.h.Server.Admin.Capabilities.Access.Gate.ListAskQuiets(direct.session) {
		direct.h.Server.Admin.Capabilities.Access.Gate.RevokeAskQuiet(q.ID)
	}
	if !direct.h.Server.Admin.Capabilities.Installation.Loopback.GrantChat(direct.session, []uint16{uint16(directHTTPPort)}, "direct-http", "test-http", nil) {
		t.Fatal("direct http loopback grant failed")
	}
	runCommandWithApprovals(t, direct, "recommended", "recommended")
	if got := direct.outcome(t, "program-1"); got.Status != 0 || !strings.Contains(got.Stdout, directHTTPOID) {
		t.Fatalf("direct_ip ls-remote failed: %+v", got)
	}
	requireDirectIPAuthzRows(t, direct)

	// 4. Retained background (&) job lifetime keeps the managed SSH service open until completion.
	bg := p.programWithArgs(t, map[string]any{
		"command":            "git fetch origin &\nwait $!",
		"capability_request": map[string]any{"socket_paths": []any{agentSock}},
	}, wire.CreateSessionRequest{}, "")
	gittest.InitCommit(t, bg.dir, "bg-init")
	gittest.Run(t, bg.dir, "remote", "add", "origin", remoteURL)
	grantSSHAuthority(t, bg, agentSock, sshFixture.port)
	runCommandWithApprovals(t, bg)
	if got := bg.outcome(t, "program-1"); got.Status != 0 {
		t.Fatalf("retained background git fetch failed: %+v", got)
	}
	if gotBG := strings.TrimSpace(gittest.Run(t, bg.dir, "rev-parse", "refs/remotes/origin/main")); gotBG != seedOID {
		t.Fatalf("background fetch ref = %q, want %q", gotBG, seedOID)
	}

	// 5. Launch failure and command cancellation release descriptors and remove service sockets.
	warmupSvc, err := gitexec.StartAgentSSH(context.Background(), nil)
	testutil.FailErr(t, "warmup StartAgentSSH", err)
	testutil.FailErr(t, "warmup Close", warmupSvc.Close())

	baselineFDs := countOpenFDs(t)
	var socketPaths []string
	for range 8 {
		svc, err := gitexec.StartAgentSSH(context.Background(), nil)
		testutil.FailErr(t, "StartAgentSSH", err)
		socketPaths = append(socketPaths, svc.Socket())
		// Simulate pre-launch abort before Bind is ever called.
		testutil.FailErr(t, "Close aborted service", svc.Close())
	}
	runtime.GC()
	if afterFailFDs := countOpenFDs(t); afterFailFDs > baselineFDs+2 {
		t.Fatalf("launch-failure leaked descriptors: baseline=%d after=%d", baselineFDs, afterFailFDs)
	}
	for _, sp := range socketPaths {
		if _, statErr := os.Stat(filepath.Dir(sp)); !os.IsNotExist(statErr) {
			t.Fatalf("service directory survived Close: %s (err=%v)", filepath.Dir(sp), statErr)
		}
	}

	// Command cancellation while OpenSSH is mid-handshake through the managed service.
	sshFixture.stallSession.Store(true)
	defer sshFixture.stallSession.Store(false)

	cancelArgs := map[string]any{
		"command":            "git ls-remote " + remoteURL,
		"capability_request": map[string]any{"socket_paths": []any{agentSock}},
	}
	var cancelEntries []llm.MockResponseEntry
	for idx := range 5 {
		cancelEntries = append(cancelEntries, llm.MockResponseEntry{
			Pattern:      fmt.Sprintf("cancel-run-%d", idx),
			ToolCalls:    []llm.MockToolCall{{ID: fmt.Sprintf("cancel-%d", idx), Name: "command", Args: cancelArgs}},
			FollowUpText: "Done.",
		})
	}
	cancelSess := p.withClient(t, llm.NewMockProvider(&llm.MockConfig{Responses: cancelEntries}), wire.CreateSessionRequest{}, "")
	cancelSess.h.SeedProgress(t, t.Context(), cancelSess.session)
	gittest.InitCommit(t, cancelSess.dir, "cancel-init")
	grantSSHAuthority(t, cancelSess, agentSock, sshFixture.port)

	runAndCancelStalled := func(idx int) {
		runCtx, cancelRun := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() {
			_, err := cancelSess.h.Sessions.Manager.Submissions.Prompt(runCtx, cancelSess.session, fmt.Sprintf("cancel-run-%d", idx))
			done <- err
		}()
		select {
		case <-sshFixture.stallEntered:
		case <-time.After(10 * time.Second):
			cancelRun()
			t.Fatal("stalled SSH session never reached upstream")
		}
		cancelRun()
		<-done
	}

	// Warm per-session output retention and socket relay handles before measuring.
	runAndCancelStalled(0)
	runtime.GC()
	cancelBaseline := countOpenFDs(t)
	for idx := 1; idx < 5; idx++ {
		runAndCancelStalled(idx)
	}
	testutil.WaitFor(t, "cancelled SSH commands release descriptors", func() bool {
		runtime.GC()
		return countOpenFDs(t) <= cancelBaseline+2
	})
}
