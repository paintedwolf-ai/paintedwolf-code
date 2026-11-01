package confine_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/session/approvalstate"
	"github.com/lycaon/lycaon/internal/testutil"
)

func requireDarwinSeatbelt(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "darwin" || !confine.Available() {
		t.Skip("darwin + seatbelt sandbox required")
	}
	warmPythonShim.Do(func() {
		_, _ = exec.Command("/usr/bin/python3", "-c", "pass").CombinedOutput()
	})
	self, err := os.Executable()
	testutil.FailErr(t, "os.Executable failed", err)
	return self
}

type socketProbeServer struct {
	ln       net.Listener
	receipts atomic.Int64
	bytes    atomic.Int64
	reply    string
}

func startProbeServer(t *testing.T, sockPath, reply string) *socketProbeServer {
	t.Helper()
	_ = os.Remove(sockPath)
	ln, err := net.Listen("unix", sockPath)
	testutil.FailErr(t, "listen unix", err)
	srv := &socketProbeServer{ln: ln, reply: reply}
	go func() {
		for {
			conn, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			go func(c net.Conn) {
				defer func() { _ = c.Close() }()
				buf, _ := io.ReadAll(c)
				if len(buf) > 0 {
					srv.receipts.Add(1)
					srv.bytes.Add(int64(len(buf)))
					_, _ = c.Write([]byte(srv.reply))
				}
			}(conn)
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		_ = os.Remove(sockPath)
	})
	return srv
}

func confinedSocketProbe(t *testing.T, self string, c confine.Confinement, targetPath, payload string) (int, string) {
	t.Helper()
	script := `import socket, sys
s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
s.settimeout(2.0)
s.connect(sys.argv[1])
s.sendall(sys.argv[2].encode())
s.shutdown(socket.SHUT_WR)
data = s.recv(1024)
if not data:
    sys.exit(2)
sys.stdout.write(data.decode())`
	code, out, _ := confinedStdout(t, self, c, "/usr/bin/python3", "-c", script, targetPath, payload)
	return code, out
}

func TestResolveSocketRequestLive(t *testing.T) {
	dir := shortTempDir(t)
	sockPath := filepath.Join(dir, "live.sock")
	startProbeServer(t, sockPath, "ack")

	grant, err := confine.ResolveSocketRequest(sockPath)
	testutil.FailErr(t, "ResolveSocketRequest", err)
	if grant.ApprovedPath != sockPath {
		t.Fatalf("approved = %q want %q", grant.ApprovedPath, sockPath)
	}
	resolved, err := filepath.EvalSymlinks(sockPath)
	testutil.FailErr(t, "EvalSymlinks", err)
	if grant.ResolvedPath != resolved {
		t.Fatalf("resolved = %q want %q", grant.ResolvedPath, resolved)
	}
	if runtime.GOOS == "darwin" {
		if grant.Listener.IsZero() {
			t.Fatal("expected listener identity to be recorded on approval")
		}
		hardLink := filepath.Join(dir, "hard.sock")
		testutil.FailErr(t, "link socket", os.Link(sockPath, hardLink))
		if _, err := confine.ResolveSocketRequest(hardLink); err == nil {
			t.Fatal("expected hard-linked socket to be refused at approval")
		}
	}

	relayGrant := grant
	relayGrant.RelayPath = filepath.Join(dir, "relay.sock")
	c := &confine.Confinement{
		Roots:        []string{dir},
		Network:      confine.NetworkDeny,
		SocketGrants: []confine.SocketGrant{relayGrant},
	}
	prof, err := confine.BuildSeatbeltProfile(*c)
	testutil.FailErr(t, "BuildSeatbeltProfile relayGrant", err)
	if !strings.Contains(prof, relayGrant.RelayPath) {
		t.Fatalf("expected profile to contain relay path %q", relayGrant.RelayPath)
	}
	lease, err := confine.BindAction(c, confine.EgressCommand{SessionID: "sess-live", ToolName: "command"})
	testutil.FailErr(t, "BindAction", err)
	_ = lease.SocketRefusal()
	rt := approvalstate.NewSocketCapabilityRuntime()
	rt.ForgetSession("sess-live")
	lease.Close(t.Context())
}

func TestRevalidateSocketGrantChanged(t *testing.T) {
	dir := shortTempDir(t)
	sockPath := filepath.Join(dir, "a.sock")
	srv := startProbeServer(t, sockPath, "ack")

	grant, err := confine.ResolveSocketRequest(sockPath)
	testutil.FailErr(t, "ResolveSocketRequest", err)
	testutil.FailErr(t, "RevalidateSocketGrant", confine.RevalidateSocketGrant(grant))

	_ = srv.ln.Close()
	_ = os.Remove(sockPath)
	if err := confine.RevalidateSocketGrant(grant); err == nil {
		t.Fatal("expected changed error after socket removal")
	}

	decoyPath := filepath.Join(dir, "b.sock")
	startProbeServer(t, decoyPath, "decoy")
	testutil.FailErr(t, "symlink repoint", os.Symlink(decoyPath, sockPath))
	if err := confine.RevalidateSocketGrant(grant); err == nil {
		t.Fatal("expected changed error after symlink repoint")
	}
	_, _ = confine.BuildSeatbeltProfile(confine.Confinement{
		Roots:        []string{dir},
		Network:      confine.NetworkDeny,
		SocketGrants: []confine.SocketGrant{{ApprovedPath: sockPath, ResolvedPath: grant.ResolvedPath}},
	})
	if rej := confine.SocketRelayRefusal(confine.Confinement{SocketGrants: []confine.SocketGrant{grant}}); rej == nil || rej.Code != confine.SocketResolveChanged {
		t.Fatalf("expected SocketRelayRefusal %q, got %+v", confine.SocketResolveChanged, rej)
	}
}

func runSocketSwapProbeCase(t *testing.T, self, name string, mutate func(t *testing.T, dir, approvedPath, decoyPath string)) {
	t.Helper()
	dir := shortTempDir(t)
	parentA := filepath.Join(dir, "a")
	parentB := filepath.Join(dir, "b")
	testutil.FailErr(t, "mkdir a", os.MkdirAll(parentA, 0o755))
	testutil.FailErr(t, "mkdir b", os.MkdirAll(parentB, 0o755))
	approvedPath := filepath.Join(parentA, "svc.sock")
	decoyPath := filepath.Join(parentB, "svc.sock")

	legit := startProbeServer(t, approvedPath, "legit-ok")
	decoy := startProbeServer(t, decoyPath, "decoy-hijack")

	grant, err := confine.ResolveSocketRequest(approvedPath)
	testutil.FailErr(t, "ResolveSocketRequest", err)

	c := &confine.Confinement{
		Roots:        []string{dir},
		Network:      confine.NetworkDeny,
		SocketGrants: []confine.SocketGrant{grant},
	}
	lease, err := confine.BindAction(c, confine.EgressCommand{SessionID: "sess-" + name, ToolName: "command"})
	testutil.FailErr(t, "BindAction", err)
	defer lease.Close(t.Context())

	mutate(t, dir, approvedPath, decoyPath)

	code, out := confinedSocketProbe(t, self, *c, approvedPath, "secret-token")
	if code == 0 || strings.Contains(out, "decoy") {
		t.Fatalf("expected connection refusal after %s: code=%d out=%q", name, code, out)
	}
	if decoy.receipts.Load() != 0 || decoy.bytes.Load() != 0 {
		t.Fatalf("decoy received traffic: receipts=%d bytes=%d", decoy.receipts.Load(), decoy.bytes.Load())
	}
	if legit.receipts.Load() != 0 || legit.bytes.Load() != 0 {
		t.Fatalf("legit received unexpected traffic: receipts=%d bytes=%d", legit.receipts.Load(), legit.bytes.Load())
	}
	rej := lease.SocketRefusal()
	if rej == nil || rej.Code != confine.SocketResolveChanged {
		t.Fatalf("expected structured refusal %q, got %+v", confine.SocketResolveChanged, rej)
	}
	if globalRej := confine.SocketRelayRefusal(*c); globalRej == nil || globalRej.Code != confine.SocketResolveChanged {
		t.Fatalf("expected SocketRelayRefusal %q, got %+v", confine.SocketResolveChanged, globalRej)
	}
}

func TestSeatbeltSocketRelayThreeProbeCases(t *testing.T) {
	self := requireDarwinSeatbelt(t)
	t.Run("symlink_planted_at_approved_path", func(t *testing.T) {
		runSocketSwapProbeCase(t, self, "symlink", func(t *testing.T, _, approvedPath, decoyPath string) {
			testutil.FailErr(t, "remove approved", os.Remove(approvedPath))
			testutil.FailErr(t, "plant symlink", os.Symlink(decoyPath, approvedPath))
		})
	})
	t.Run("hardlink_planted_at_approved_path", func(t *testing.T) {
		runSocketSwapProbeCase(t, self, "hardlink", func(t *testing.T, _, approvedPath, decoyPath string) {
			testutil.FailErr(t, "remove approved", os.Remove(approvedPath))
			testutil.FailErr(t, "plant hardlink", os.Link(decoyPath, approvedPath))
		})
	})
	t.Run("approved_socket_renamed_and_decoy_renamed_into_place", func(t *testing.T) {
		runSocketSwapProbeCase(t, self, "rename", func(t *testing.T, dir, approvedPath, decoyPath string) {
			aside := filepath.Join(dir, "aside.sock")
			testutil.FailErr(t, "rename approved aside", os.Rename(approvedPath, aside))
			testutil.FailErr(t, "rename decoy into place", os.Rename(decoyPath, approvedPath))
		})
	})
	t.Run("parent_directory_swapped", func(t *testing.T) {
		runSocketSwapProbeCase(t, self, "dirswap", func(t *testing.T, dir, approvedPath, decoyPath string) {
			parentA := filepath.Dir(approvedPath)
			parentB := filepath.Dir(decoyPath)
			asideDir := filepath.Join(dir, "parent-aside")
			testutil.FailErr(t, "rename parentA aside", os.Rename(parentA, asideDir))
			testutil.FailErr(t, "rename parentB to parentA", os.Rename(parentB, parentA))
		})
	})
}

func TestSeatbeltSocketRelayProjectLocalConcurrentAndCleanup(t *testing.T) {
	self := requireDarwinSeatbelt(t)
	proj := shortTempDir(t)
	sockPath := filepath.Join(proj, "local.sock")
	srv := startProbeServer(t, sockPath, "pong")

	grant, err := confine.ResolveSocketRequest(sockPath)
	testutil.FailErr(t, "ResolveSocketRequest", err)

	rt := approvalstate.NewSocketCapabilityRuntime()
	c := &confine.Confinement{
		Roots:        []string{proj},
		Network:      confine.NetworkDeny,
		SocketGrants: []confine.SocketGrant{grant},
	}
	lease, err := confine.BindAction(c, confine.EgressCommand{SessionID: "sess-local", ToolName: "command"})
	testutil.FailErr(t, "BindAction", err)
	defer lease.Close(t.Context())

	relayPath := c.SocketGrants[0].RelayPath
	if relayPath == "" || relayPath == grant.ResolvedPath {
		t.Fatalf("expected distinct engine-owned relay path, got %q", relayPath)
	}
	profile, err := confine.BuildSeatbeltProfile(*c)
	testutil.FailErr(t, "BuildSeatbeltProfile", err)
	if !strings.Contains(profile, `(allow network-outbound (literal "`+relayPath+`"))`) {
		t.Fatalf("profile missing relay literal:\n%s", profile)
	}
	if strings.Contains(profile, `(allow network-outbound (literal "`+grant.ResolvedPath+`"))`) {
		t.Fatalf("profile must not expose destination socket path directly:\n%s", profile)
	}
	if code := confinedExit(t, self, *c, "/bin/mv", relayPath, relayPath+".moved"); code == 0 {
		t.Fatal("confined process must not be able to rename the relay socket")
	}

	var wg sync.WaitGroup
	const workers = 4
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			code, out := confinedSocketProbe(t, self, *c, sockPath, fmt.Sprintf("ping-%d", idx))
			if code != 0 || out != "pong" {
				errs <- fmt.Errorf("worker %d: code=%d out=%q", idx, code, out)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		testutil.FailErr(t, "concurrent relay probe", e)
	}
	if srv.receipts.Load() != workers {
		t.Fatalf("expected %d receipts, got %d", workers, srv.receipts.Load())
	}

	// Unbound command confinement also opens and cleans up per-command relays.
	unbound := confine.Confinement{Roots: []string{proj}, Network: confine.NetworkDeny, SocketGrants: []confine.SocketGrant{grant}}
	if code, out := confinedSocketProbe(t, self, unbound, sockPath, "unbound"); code != 0 || out != "pong" {
		t.Fatalf("unbound command relay probe failed: code=%d out=%q", code, out)
	}

	relayDir := filepath.Dir(relayPath)
	rt.ForgetSession("sess-local")
	if _, statErr := os.Lstat(relayDir); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("expected relay directory %q removed on session end, err=%v", relayDir, statErr)
	}
}

func startExternalPythonListener(t *testing.T, dir, sockPath string) string {
	t.Helper()
	recFile := filepath.Join(dir, "py-received")
	readyFile := filepath.Join(dir, "py-ready")
	pyServer := `import socket, sys
sock_path, ready_path, rec_path = sys.argv[1], sys.argv[2], sys.argv[3]
s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
s.bind(sock_path)
s.listen(4)
open(ready_path, "w").write("1")
while True:
    conn, _ = s.accept()
    data = conn.recv(1024)
    if data:
        open(rec_path, "wb").write(data)
        conn.sendall(b"py-approved")
    conn.close()`
	ctx, cancel := context.WithCancel(t.Context())
	pyCmd := exec.CommandContext(ctx, "/usr/bin/python3", "-c", pyServer, sockPath, readyFile, recFile)
	testutil.FailErr(t, "start python listener", pyCmd.Start())
	t.Cleanup(func() {
		cancel()
		_ = pyCmd.Wait()
	})
	for {
		if _, statErr := os.Stat(readyFile); statErr == nil {
			return recFile
		}
		if pyCmd.ProcessState != nil {
			t.Fatal("python listener exited early")
		}
	}
}

func TestSeatbeltSocketRelayDaemonRestartSameAndDifferentIdentity(t *testing.T) {
	self := requireDarwinSeatbelt(t)
	dir := shortTempDir(t)
	sockPath := filepath.Join(dir, "daemon.sock")
	srv1 := startProbeServer(t, sockPath, "v1")

	grant, err := confine.ResolveSocketRequest(sockPath)
	testutil.FailErr(t, "ResolveSocketRequest", err)

	c := &confine.Confinement{
		Roots:        []string{dir},
		Network:      confine.NetworkDeny,
		SocketGrants: []confine.SocketGrant{grant},
	}
	lease, err := confine.BindAction(c, confine.EgressCommand{SessionID: "sess-restart", ToolName: "command"})
	testutil.FailErr(t, "BindAction", err)
	defer lease.Close(t.Context())

	if code, out := confinedSocketProbe(t, self, *c, sockPath, "hello"); code != 0 || out != "v1" {
		t.Fatalf("initial connection failed: code=%d out=%q", code, out)
	}

	// Restart daemon at the same path with the same UID and code-signing identity.
	_ = srv1.ln.Close()
	_ = os.Remove(sockPath)
	srv2 := startProbeServer(t, sockPath, "v2")
	if code, out := confinedSocketProbe(t, self, *c, sockPath, "hello-again"); code != 0 || out != "v2" || srv2.receipts.Load() != 1 {
		t.Fatalf("same-identity daemon restart must keep grant: code=%d out=%q receipts=%d", code, out, srv2.receipts.Load())
	}

	// Replace daemon at the same path with a different code-signing identity (/usr/bin/python3).
	_ = srv2.ln.Close()
	_ = os.Remove(sockPath)
	recFile := startExternalPythonListener(t, dir, sockPath)

	if code, out := confinedSocketProbe(t, self, *c, sockPath, "must-not-forward"); code == 0 || strings.Contains(out, "py-approved") {
		t.Fatalf("different-identity listener must be refused: code=%d out=%q", code, out)
	}
	if _, statErr := os.Stat(recFile); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatal("different-identity listener must receive zero bytes")
	}
	if rej := lease.SocketRefusal(); rej == nil || rej.Code != confine.SocketResolveChanged {
		t.Fatalf("expected SocketResolveChanged refusal, got %+v", rej)
	}

	// Fresh approval for the new listener identity permits connections again.
	reapproved, err := confine.ResolveSocketRequest(sockPath)
	testutil.FailErr(t, "ResolveSocketRequest reapproved", err)
	c2 := &confine.Confinement{Roots: []string{dir}, Network: confine.NetworkDeny, SocketGrants: []confine.SocketGrant{reapproved}}
	lease2, err := confine.BindAction(c2, confine.EgressCommand{SessionID: "sess-restart-2", ToolName: "command"})
	testutil.FailErr(t, "BindAction reapproved", err)
	defer lease2.Close(t.Context())
	if code, out := confinedSocketProbe(t, self, *c2, sockPath, "after-reapproval"); code != 0 || out != "py-approved" {
		t.Fatalf("reapproved listener must succeed: code=%d out=%q", code, out)
	}
}
