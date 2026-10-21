package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/launchverify"
	"github.com/lycaon/lycaon/internal/pkgmanifest"
	"github.com/lycaon/lycaon/internal/platformfloor"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/vaultidentity"
)

var (
	engineImage  = []byte("engine")
	desktopImage = []byte("desktop app")
)

type fixture struct {
	t                *testing.T
	base             string
	procRoot         string
	generationsDir   string
	policyPath       string
	locksDir         string
	activeGeneration string
	state            string
	peer             Peer
	server           *Server
	socket           string
	cancel           context.CancelFunc
	done             chan error
}

type process struct {
	pid, ppid, uid int
	start          uint64
	image          []byte
	generation     string
	exePath        string
}

func (f *fixture) addProcess(p process) {
	dir := filepath.Join(f.procRoot, strconv.Itoa(p.pid))
	testutil.FailErr(f.t, "mkdir proc", os.MkdirAll(dir, 0o755))
	stat := fmt.Sprintf("%d (x) S %d 1 1 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 %d 1 1 1", p.pid, p.ppid, p.start)
	testutil.FailErr(f.t, "write stat", os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o644))
	status := fmt.Sprintf("TracerPid:\t0\nUid:\t%d\t%d\t%d\t%d\n", p.uid, p.uid, p.uid, p.uid)
	testutil.FailErr(f.t, "write status", os.WriteFile(filepath.Join(dir, "status"), []byte(status), 0o644))
	target := p.exePath
	gen := p.generation
	if gen == "" {
		gen = f.activeGeneration
	}
	if target == "" && gen != "" {
		switch {
		case bytes.Equal(p.image, engineImage):
			target = filepath.Join(f.generationsDir, gen, "bin/pw")
		case bytes.Equal(p.image, desktopImage):
			target = filepath.Join(f.generationsDir, gen, "bin/painted-wolf-code")
		}
	}
	if target == "" {
		target = filepath.Join(f.procRoot, "images", strconv.Itoa(p.pid))
		testutil.FailErr(f.t, "mkdir images", os.MkdirAll(filepath.Dir(target), 0o755))
		testutil.FailErr(f.t, "write exe image", os.WriteFile(target, p.image, 0o755))
	}
	testutil.FailErr(f.t, "link exe", os.Symlink(target, filepath.Join(dir, "exe")))
	testutil.FailErr(f.t, "create target root", os.MkdirAll(filepath.Join(dir, "root"), 0o755))
	// Only anonymous code: the mapping rules are covered in launchverify.
	maps := "7fff00000000-7fff00002000 r-xp 00000000 00:00 0                          [vdso]\n"
	testutil.FailErr(f.t, "write maps", os.WriteFile(filepath.Join(dir, "maps"), []byte(maps), 0o644))
}

func (f *fixture) removeProcess(pid int) {
	testutil.FailErr(f.t, "remove proc", os.RemoveAll(filepath.Join(f.procRoot, strconv.Itoa(pid))))
}

func entry(path string, image []byte) pkgmanifest.Executable {
	sum := sha256.Sum256(image)
	return pkgmanifest.Executable{Path: path, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(image))}
}

func marshalManifest(t *testing.T, version string, executables ...pkgmanifest.Executable) []byte {
	t.Helper()
	const installRoot = "/opt/painted-wolf-code"
	payload := map[string]pkgmanifest.PayloadEntry{}
	for _, exe := range executables {
		relative := strings.TrimPrefix(exe.Path, installRoot+"/")
		for parent := filepath.Dir(relative); parent != "."; parent = filepath.Dir(parent) {
			payload[parent] = pkgmanifest.PayloadEntry{Path: parent, Kind: "directory", Mode: 0o755}
		}
		sum, size := exe.SHA256, exe.Size
		payload[relative] = pkgmanifest.PayloadEntry{Path: relative, Kind: "file", Mode: 0o755, SHA256: &sum, Size: &size}
	}
	entries := make([]pkgmanifest.PayloadEntry, 0, len(payload))
	for _, row := range payload {
		entries = append(entries, row)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	arch, ok := platformfloor.ELFArchForGoArch(runtime.GOARCH)
	if !ok {
		arch = "x86_64"
	}
	raw, err := json.Marshal(pkgmanifest.Manifest{
		FormatVersion: pkgmanifest.FormatVersion, Package: "painted-wolf-code", Version: version,
		Architecture: arch, InstallRoot: installRoot, Executables: executables, Payload: entries,
	})
	testutil.FailErr(t, "encode manifest", err)
	return raw
}

func (f *fixture) stageGeneration(version string, desktop, engine []byte) string {
	f.t.Helper()
	var executables []pkgmanifest.Executable
	if desktop != nil {
		executables = append(executables, entry(launchverify.DesktopAppPath, desktop))
	}
	if engine != nil {
		executables = append(executables, entry(launchverify.EnginePath, engine))
	}
	raw := marshalManifest(f.t, version, executables...)
	sum := sha256.Sum256(raw)
	sha := hex.EncodeToString(sum[:])
	genDir := filepath.Join(f.generationsDir, sha)
	testutil.FailErr(f.t, "mkdir generation bin", os.MkdirAll(filepath.Join(genDir, "bin"), 0o755))
	testutil.FailErr(f.t, "chmod generations dir", os.Chmod(f.generationsDir, 0o755))
	testutil.FailErr(f.t, "chmod generation dir", os.Chmod(genDir, 0o755))
	testutil.FailErr(f.t, "chmod generation bin", os.Chmod(filepath.Join(genDir, "bin"), 0o755))
	if desktop != nil {
		testutil.FailErr(f.t, "write desktop", os.WriteFile(filepath.Join(genDir, "bin/painted-wolf-code"), desktop, 0o755))
		testutil.FailErr(f.t, "chmod desktop", os.Chmod(filepath.Join(genDir, "bin/painted-wolf-code"), 0o755))
	}
	if engine != nil {
		testutil.FailErr(f.t, "write engine", os.WriteFile(filepath.Join(genDir, "bin/pw"), engine, 0o755))
		testutil.FailErr(f.t, "chmod engine", os.Chmod(filepath.Join(genDir, "bin/pw"), 0o755))
	}
	testutil.FailErr(f.t, "write manifest", os.WriteFile(filepath.Join(genDir, "manifest.json"), raw, 0o644))
	testutil.FailErr(f.t, "chmod manifest", os.Chmod(filepath.Join(genDir, "manifest.json"), 0o644))
	return sha
}

func (f *fixture) activateGeneration(sha string) {
	f.t.Helper()
	raw, err := json.Marshal(struct {
		Version            int    `json:"version"`
		GenerationRoot     string `json:"generation_root"`
		ManifestSHA256     string `json:"manifest_sha256"`
		LogicalInstallRoot string `json:"logical_install_root"`
	}{
		Version:            1,
		GenerationRoot:     filepath.Join(f.generationsDir, sha),
		ManifestSHA256:     sha,
		LogicalInstallRoot: "/opt/painted-wolf-code",
	})
	testutil.FailErr(f.t, "marshal policy", err)
	testutil.FailErr(f.t, "write policy", os.WriteFile(f.policyPath, raw, 0o644))
	testutil.FailErr(f.t, "chmod policy", os.Chmod(f.policyPath, 0o644))
	f.activeGeneration = sha
}

func (f *fixture) lockGeneration(sha string) *os.File {
	f.t.Helper()
	testutil.FailErr(f.t, "mkdir locks", os.MkdirAll(f.locksDir, 0o755))
	testutil.FailErr(f.t, "chmod locks", os.Chmod(f.locksDir, 0o755))
	lockPath := filepath.Join(f.locksDir, sha+".lock")
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	testutil.FailErr(f.t, "open lock", err)
	testutil.FailErr(f.t, "chmod lock", os.Chmod(lockPath, 0o600))
	testutil.FailErr(f.t, "flock SH", syscall.Flock(int(file.Fd()), syscall.LOCK_SH|syscall.LOCK_NB))
	f.t.Cleanup(func() { _ = file.Close() })
	return file
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	testutil.FailErr(t, "chmod root", os.Chmod(root, 0o755))
	sockDir, err := os.MkdirTemp("", "vi") //nolint:usetesting // Short socket path.
	testutil.FailErr(t, "MkdirTemp", err)
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	f := &fixture{
		t:              t,
		base:           root,
		procRoot:       filepath.Join(root, "proc"),
		generationsDir: filepath.Join(root, "generations"),
		policyPath:     filepath.Join(root, "launch.json"),
		locksDir:       filepath.Join(root, "locks"),
		state:          filepath.Join(root, "state"),
		peer:           Peer{UID: 1000, PID: 301},
		socket:         filepath.Join(sockDir, "s.sock"),
	}
	sha := f.stageGeneration("1.0.2", desktopImage, engineImage)
	f.activateGeneration(sha)
	f.addProcess(process{pid: 300, ppid: 1, uid: 1000, start: 10, image: desktopImage})
	f.addProcess(process{pid: 301, ppid: 300, uid: 1000, start: 20, image: engineImage})
	f.server = &Server{
		Store: &Store{Root: f.state},
		Verifier: launchverify.Verifier{
			ProcRoot:       f.procRoot,
			GenerationsDir: f.generationsDir,
			PolicyPath:     f.policyPath,
			LocksDir:       f.locksDir,
			TrustAnchor:    root,
			TrustedOwner:   uint32(os.Geteuid()),
			NamespaceReader: func(_ string, pid int) (launchverify.NamespaceFacts, error) {
				if pid == 0 {
					return launchverify.NamespaceFacts{Device: 1, Inode: 10}, nil
				}
				return launchverify.NamespaceFacts{Device: 1, Inode: 20, FullUIDMap: true, FullGIDMap: true, NoNewPrivileges: true}, nil
			},
		},
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
			testutil.FailErr(f.t, "remove manifest", os.Remove(filepath.Join(f.generationsDir, f.activeGeneration, "manifest.json")))
		}},
		{name: "manifest without the desktop app", status: vaultidentity.StatusUnavailable, code: launchverify.CodeManifestUnreadable, setup: func(f *fixture) {
			sha := f.stageGeneration("1.0.3", nil, engineImage)
			f.activateGeneration(sha)
		}},
		{name: "unlocked retired generation", status: vaultidentity.StatusRefused, code: launchverify.CodeExecutableMismatch, setup: func(f *fixture) {
			shaB := f.stageGeneration("1.0.3", []byte("desktop B"), []byte("engine B"))
			f.activateGeneration(shaB)
		}},
		{name: "unlinked binary never falls back to top-level manifest", status: vaultidentity.StatusRefused, code: launchverify.CodeExecutableMismatch, setup: func(f *fixture) {
			topManifest := filepath.Join(f.base, "manifest.json")
			testutil.FailErr(f.t, "write top manifest", os.WriteFile(topManifest, marshalManifest(f.t, "1.0.2", entry(launchverify.DesktopAppPath, desktopImage), entry(launchverify.EnginePath, engineImage)), 0o644))
			outside := filepath.Join(f.base, "outside-pw")
			testutil.FailErr(f.t, "write outside engine", os.WriteFile(outside, engineImage, 0o755))
			f.removeProcess(301)
			f.addProcess(process{pid: 301, ppid: 300, uid: 1000, start: 20, image: engineImage, exePath: outside})
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
	if _, err := listen(t.Context(), ""); err == nil {
		t.Fatal("listened without an activated socket")
	}
	t.Setenv("LISTEN_PID", strconv.Itoa(os.Getpid()))
	t.Setenv("LISTEN_FDS", "2")
	if _, err := listen(t.Context(), ""); err == nil {
		t.Fatal("accepted two activated sockets")
	}
	dir, err := os.MkdirTemp("", "vi") //nolint:usetesting // Short socket path.
	testutil.FailErr(t, "MkdirTemp", err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	listener, err := listen(t.Context(), filepath.Join(dir, "m.sock"))
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
