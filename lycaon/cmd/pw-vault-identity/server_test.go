package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/launchverify"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/vaultidentity"
)

var (
	engineImage  = []byte("engine")
	desktopImage = []byte("desktop app")
)

type fixture struct {
	t        *testing.T
	procRoot string
	manifest string
	state    string
	peer     Peer
	server   *Server
	socket   string
	cancel   context.CancelFunc
	done     chan error
}

type process struct {
	pid, ppid, uid int
	start          uint64
	image          []byte
}

func (f *fixture) addProcess(p process) {
	dir := filepath.Join(f.procRoot, strconv.Itoa(p.pid))
	testutil.FailErr(f.t, "mkdir proc", os.MkdirAll(dir, 0o755))
	stat := fmt.Sprintf("%d (x) S %d 1 1 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 %d 1 1 1", p.pid, p.ppid, p.start)
	testutil.FailErr(f.t, "write stat", os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o644))
	status := fmt.Sprintf("Uid:\t%d\t%d\t%d\t%d\n", p.uid, p.uid, p.uid, p.uid)
	testutil.FailErr(f.t, "write status", os.WriteFile(filepath.Join(dir, "status"), []byte(status), 0o644))
	testutil.FailErr(f.t, "write exe", os.WriteFile(filepath.Join(dir, "exe"), p.image, 0o755))
	// Only anonymous code: the mapping rules are covered in launchverify.
	maps := "7fff00000000-7fff00002000 r-xp 00000000 00:00 0                          [vdso]\n"
	testutil.FailErr(f.t, "write maps", os.WriteFile(filepath.Join(dir, "maps"), []byte(maps), 0o644))
}

func (f *fixture) removeProcess(pid int) {
	testutil.FailErr(f.t, "remove proc", os.RemoveAll(filepath.Join(f.procRoot, strconv.Itoa(pid))))
}

func digest(image []byte) string {
	sum := sha256.Sum256(image)
	return launchverify.DigestString(sum[:])
}

func writeManifest(t *testing.T, path string, executables map[string]string) {
	t.Helper()
	raw, err := json.Marshal(launchverify.Manifest{Version: "1.0.2", Executables: executables})
	testutil.FailErr(t, "encode manifest", err)
	testutil.FailErr(t, "write manifest", os.WriteFile(path, raw, 0o644))
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	sockDir, err := os.MkdirTemp("", "vi") //nolint:usetesting // Short socket path.
	testutil.FailErr(t, "MkdirTemp", err)
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	f := &fixture{
		t: t, procRoot: filepath.Join(root, "proc"), manifest: filepath.Join(root, "manifest.json"),
		state: filepath.Join(root, "state"), peer: Peer{UID: 1000, PID: 301}, socket: filepath.Join(sockDir, "s.sock"),
	}
	writeManifest(t, f.manifest, map[string]string{
		"painted-wolf-code": digest(desktopImage), "engine/pw": digest(engineImage),
	})
	f.addProcess(process{pid: 300, ppid: 1, uid: 1000, start: 10, image: desktopImage})
	f.addProcess(process{pid: 301, ppid: 300, uid: 1000, start: 20, image: engineImage})
	f.server = &Server{
		Store: &Store{Root: f.state}, Verifier: launchverify.Verifier{ProcRoot: f.procRoot}, Manifest: f.manifest,
		PeerOf: func(net.Conn) (Peer, error) { return f.peer, nil },
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	return f
}

func (f *fixture) start() *vaultidentity.Client {
	listener, err := net.Listen("unix", f.socket)
	testutil.FailErr(f.t, "listen", err)
	ctx, cancel := context.WithCancel(context.Background())
	f.cancel, f.done = cancel, make(chan error, 1)
	go func() { f.done <- f.server.Serve(ctx, listener) }()
	client := vaultidentity.NewClient(f.socket)
	f.t.Cleanup(func() {
		_ = client.Close()
		cancel()
		testutil.FailErr(f.t, "serve", <-f.done)
	})
	return client
}

func do(t *testing.T, client *vaultidentity.Client, req vaultidentity.Request) vaultidentity.Response {
	t.Helper()
	resp, err := client.Do(req)
	testutil.FailErr(t, "request "+string(req.Op), err)
	return resp
}

func TestServiceStoresOneIdentityPerUserAndAccount(t *testing.T) {
	f := newFixture(t)
	client := f.start()
	const account = "age-x25519-identity-v1:0a"
	if resp := do(t, client, vaultidentity.Request{Op: vaultidentity.OpProbe}); resp.Status != vaultidentity.StatusOK || resp.LauncherPID != 300 {
		t.Fatalf("probe = %+v", resp)
	}
	if resp := do(t, client, vaultidentity.Request{Op: vaultidentity.OpRead, Account: account}); resp.Status != vaultidentity.StatusNotFound {
		t.Fatalf("read before create = %+v", resp)
	}
	if resp := do(t, client, vaultidentity.Request{Op: vaultidentity.OpCreate, Account: account, Value: []byte("first")}); resp.Status != vaultidentity.StatusOK {
		t.Fatalf("create = %+v", resp)
	}
	if resp := do(t, client, vaultidentity.Request{Op: vaultidentity.OpCreate, Account: account, Value: []byte("second")}); resp.Status != vaultidentity.StatusExists {
		t.Fatalf("second create = %+v", resp)
	}
	resp := do(t, client, vaultidentity.Request{Op: vaultidentity.OpRead, Account: account})
	if resp.Status != vaultidentity.StatusOK || string(resp.Value) != "first" || resp.LauncherPID != 300 {
		t.Fatalf("read = %+v", resp)
	}
	info, err := os.Stat(filepath.Join(f.state, "1000", account+".identity"))
	testutil.FailErr(t, "stat identity", err)
	dirInfo, err := os.Stat(filepath.Join(f.state, "1000"))
	testutil.FailErr(t, "stat user dir", err)
	if info.Mode().Perm() != 0o600 || dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("modes = %v %v", info.Mode(), dirInfo.Mode())
	}
	entries, err := os.ReadDir(filepath.Join(f.state, "1000"))
	testutil.FailErr(t, "list user dir", err)
	if len(entries) != 1 {
		t.Fatalf("temp files remain: %v", entries)
	}
	if resp := do(t, client, vaultidentity.Request{Op: vaultidentity.OpRemove, Account: account}); resp.Status != vaultidentity.StatusOK {
		t.Fatalf("remove = %+v", resp)
	}
	if resp := do(t, client, vaultidentity.Request{Op: vaultidentity.OpRemove, Account: account}); resp.Status != vaultidentity.StatusOK {
		t.Fatalf("remove again = %+v", resp)
	}
	if resp := do(t, client, vaultidentity.Request{Op: vaultidentity.OpRead, Account: account}); resp.Status != vaultidentity.StatusNotFound {
		t.Fatalf("read after remove = %+v", resp)
	}
}

func TestServiceKeepsUsersApart(t *testing.T) {
	f := newFixture(t)
	f.addProcess(process{pid: 400, ppid: 1, uid: 1001, start: 10, image: desktopImage})
	f.addProcess(process{pid: 401, ppid: 400, uid: 1001, start: 20, image: engineImage})
	client := f.start()
	const account = "age-x25519-identity-v1:0a"
	do(t, client, vaultidentity.Request{Op: vaultidentity.OpCreate, Account: account, Value: []byte("first user")})
	testutil.FailErr(t, "close", client.Close())
	f.peer = Peer{UID: 1001, PID: 401}
	if resp := do(t, client, vaultidentity.Request{Op: vaultidentity.OpRead, Account: account}); resp.Status != vaultidentity.StatusNotFound || resp.LauncherPID != 400 {
		t.Fatalf("other user's read = %+v", resp)
	}
}

func TestServiceRefusesUnverifiedCallersAndClosesTheConnection(t *testing.T) {
	for _, tt := range []struct {
		name   string
		setup  func(*fixture)
		status vaultidentity.Status
		code   launchverify.Code
	}{
		{name: "modified engine", status: vaultidentity.StatusRefused, code: launchverify.CodeExecutableMismatch, setup: func(f *fixture) {
			f.removeProcess(301)
			f.addProcess(process{pid: 301, ppid: 300, uid: 1000, start: 20, image: []byte("patched")})
		}},
		{name: "terminal as the parent", status: vaultidentity.StatusRefused, code: launchverify.CodeExecutableMismatch, setup: func(f *fixture) {
			f.removeProcess(300)
			f.addProcess(process{pid: 300, ppid: 1, uid: 1000, start: 10, image: []byte("bash")})
		}},
		{name: "peer uid differs from the process", status: vaultidentity.StatusRefused, code: launchverify.CodeUIDMismatch, setup: func(f *fixture) {
			f.peer.UID = 1001
		}},
		{name: "no such process", status: vaultidentity.StatusRefused, code: launchverify.CodeProcessGone, setup: func(f *fixture) {
			f.peer.PID = 999
		}},
		{name: "manifest missing", status: vaultidentity.StatusUnavailable, code: launchverify.CodeManifestUnreadable, setup: func(f *fixture) {
			testutil.FailErr(f.t, "remove manifest", os.Remove(f.manifest))
		}},
		{name: "manifest without the desktop app", status: vaultidentity.StatusUnavailable, code: launchverify.CodeManifestUnreadable, setup: func(f *fixture) {
			writeManifest(f.t, f.manifest, map[string]string{"pw": digest(engineImage)})
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			tt.setup(f)
			client := f.start()
			resp := do(t, client, vaultidentity.Request{Op: vaultidentity.OpProbe})
			if resp.Status != tt.status || resp.Code != string(tt.code) || resp.LauncherPID != 0 {
				t.Fatalf("response = %+v", resp)
			}
			// The refused connection is closed; the next request reconnects
			// and is refused again rather than served.
			if resp := do(t, client, vaultidentity.Request{Op: vaultidentity.OpProbe}); resp.Status != tt.status {
				t.Fatalf("second response = %+v", resp)
			}
		})
	}
}

func TestServiceRechecksTheCallerOnEveryRequest(t *testing.T) {
	f := newFixture(t)
	client := f.start()
	do(t, client, vaultidentity.Request{Op: vaultidentity.OpProbe})
	f.removeProcess(301)
	f.addProcess(process{pid: 301, ppid: 300, uid: 1000, start: 99, image: engineImage})
	resp := do(t, client, vaultidentity.Request{Op: vaultidentity.OpRead, Account: "a"})
	if resp.Status != vaultidentity.StatusRefused || resp.Code != string(launchverify.CodeProcessChanged) {
		t.Fatalf("reused pid = %+v", resp)
	}
}

func TestServiceRejectsMalformedRequests(t *testing.T) {
	f := newFixture(t)
	client := f.start()
	for name, req := range map[string]vaultidentity.Request{
		"bad account":  {Op: vaultidentity.OpRead, Account: "../etc"},
		"unknown op":   {Op: "steal", Account: "a"},
		"large value":  {Op: vaultidentity.OpCreate, Account: "a", Value: make([]byte, vaultidentity.MaxValueBytes+1)},
		"empty create": {Op: vaultidentity.OpCreate, Account: "a"},
	} {
		resp := do(t, client, req)
		if resp.Status != vaultidentity.StatusInvalid && !(name == "empty create" && resp.Status == vaultidentity.StatusUnavailable) {
			t.Fatalf("%s = %+v", name, resp)
		}
	}
	entries, _ := os.ReadDir(filepath.Join(f.state, "1000"))
	if len(entries) != 0 {
		t.Fatalf("invalid requests wrote %v", entries)
	}
}

func TestServiceExitsWhenIdle(t *testing.T) {
	f := newFixture(t)
	f.server.IdleExit = 40 * time.Millisecond
	listener, err := net.Listen("unix", f.socket)
	testutil.FailErr(t, "listen", err)
	done := make(chan error, 1)
	go func() { done <- f.server.Serve(context.Background(), listener) }()
	select {
	case err := <-done:
		testutil.FailErr(t, "idle serve", err)
	case <-time.After(5 * time.Second):
		t.Fatal("the service did not exit when idle")
	}
}

func TestPeerCredentialsRequireAUnixSocket(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	if _, err := peerCredentials(a); err == nil {
		t.Fatal("a pipe yielded credentials")
	}
}

func TestListenNeedsAnActivatedSocketOrAPath(t *testing.T) {
	t.Setenv("LISTEN_PID", "")
	t.Setenv("LISTEN_FDS", "")
	if _, err := listen(""); err == nil {
		t.Fatal("listened without an activated socket")
	}
	t.Setenv("LISTEN_PID", strconv.Itoa(os.Getpid()))
	t.Setenv("LISTEN_FDS", "2")
	if _, err := listen(""); err == nil {
		t.Fatal("accepted two activated sockets")
	}
	dir, err := os.MkdirTemp("", "vi") //nolint:usetesting // Short socket path.
	testutil.FailErr(t, "MkdirTemp", err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	listener, err := listen(filepath.Join(dir, "m.sock"))
	testutil.FailErr(t, "listen on path", err)
	defer listener.Close()
	info, err := os.Stat(filepath.Join(dir, "m.sock"))
	testutil.FailErr(t, "stat socket", err)
	if info.Mode().Perm() != 0o666 {
		t.Fatalf("socket mode = %v", info.Mode())
	}
	if err := run([]string{"--state", ""}); err == nil {
		t.Fatal("ran without a state directory")
	}
}
