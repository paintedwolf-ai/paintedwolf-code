package session

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	osexec "os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/lineage"
	"github.com/lycaon/lycaon/internal/testutil"
)

// provenanceFixture is a host whose socket table, launch lineage, and
// container engine the test states outright.
type provenanceFixture struct {
	atStart    []socketListener
	startErr   error
	now        []socketListener
	nowErr     error
	sessions   map[int]string
	containers []dockerContainerInfo
}

func (f *provenanceFixture) resolver(ctx context.Context) *DefaultLoopbackProvenance {
	started := false
	return newLoopbackProvenance(
		ctx,
		func(context.Context) ([]socketListener, error) {
			if !started {
				started = true
				return f.atStart, f.startErr
			}
			return f.now, f.nowErr
		},
		func(pid int) (string, bool) {
			s, ok := f.sessions[pid]
			return s, ok
		},
		func(context.Context) []dockerContainerInfo { return f.containers },
	)
}

func listenerAt(pid int, addr string) socketListener {
	ap := netip.MustParseAddrPort(addr)
	return socketListener{PID: pid, Port: ap.Port(), Addr: ap.Addr()}
}

func TestLoopbackOwnershipByLaunchLineage(t *testing.T) {
	ctx := context.Background()
	f := &provenanceFixture{
		now:      []socketListener{listenerAt(4242, "127.0.0.1:9999")},
		sessions: map[int]string{4242: "chat-1"},
	}
	owned, evidence := f.resolver(t.Context()).IsSessionOwned(ctx, "chat-1", "/workspace", 9999)
	if !owned || evidence.Method != ProvenanceLineage {
		t.Fatalf("listener launched by this session: owned=%v evidence=%+v", owned, evidence)
	}
	owned, evidence = f.resolver(t.Context()).IsSessionOwned(ctx, "chat-2", "/workspace", 9999)
	if owned {
		t.Fatalf("another session's listener was attributed to this one: %+v", evidence)
	}
}

func TestLoopbackOwnershipRefusesExposedChatListener(t *testing.T) {
	for _, addr := range []string{"[::]:9999", "0.0.0.0:9999", "192.168.1.20:9999"} {
		f := &provenanceFixture{
			now:      []socketListener{listenerAt(4242, addr)},
			sessions: map[int]string{4242: "chat-1"},
		}
		owned, evidence := f.resolver(t.Context()).IsSessionOwned(context.Background(), "chat-1", "/workspace", 9999)
		if owned || evidence.Method != ProvenanceExposed {
			t.Errorf("chat listener on %s: owned=%v evidence=%+v, want external exposure", addr, owned, evidence)
		}
	}
}

// A port that appears during the session is not the chat's merely because it
// is new: a service the person starts, or another session's, stays foreign.
func TestLoopbackOwnershipIgnoresListenersThatMerelyAppeared(t *testing.T) {
	f := &provenanceFixture{now: []socketListener{listenerAt(777, "127.0.0.1:5173")}}
	owned, evidence := f.resolver(t.Context()).IsSessionOwned(context.Background(), "chat-1", "/workspace", 5173)
	if owned {
		t.Fatalf("a listener outside every chat lineage was owned: %+v", evidence)
	}
}

// Every holder must descend from the chat; one foreign holder on the same port
// keeps it foreign.
func TestLoopbackOwnershipNeedsEveryHolder(t *testing.T) {
	f := &provenanceFixture{
		now:      []socketListener{listenerAt(4242, "127.0.0.1:9999"), listenerAt(900, "[::1]:9999")},
		sessions: map[int]string{4242: "chat-1"},
	}
	if owned, evidence := f.resolver(t.Context()).IsSessionOwned(context.Background(), "chat-1", "/workspace", 9999); owned {
		t.Fatalf("a port shared with a foreign process was owned: %+v", evidence)
	}
}

// A workspace compose file is agent-writable text; it never establishes ownership.
func TestLoopbackOwnershipIgnoresComposeManifests(t *testing.T) {
	workspace := t.TempDir()
	manifest := "services:\n  web:\n    ports:\n      - \"127.0.0.1:3001:80\"\n"
	testutil.FailErr(t, "write compose manifest", os.WriteFile(filepath.Join(workspace, "compose.yaml"), []byte(manifest), 0o600))
	f := &provenanceFixture{now: []socketListener{listenerAt(555, "127.0.0.1:3001")}}
	if owned, evidence := f.resolver(t.Context()).IsSessionOwned(context.Background(), "chat-1", workspace, 3001); owned {
		t.Fatalf("a compose manifest established ownership: %+v", evidence)
	}
}

func workspaceContainer(workspace string, mappings ...containerPortMapping) dockerContainerInfo {
	return dockerContainerInfo{Name: "app-web-1", WorkingDir: workspace, Ports: mappings}
}

func TestLoopbackOwnershipByWorkspaceContainer(t *testing.T) {
	ctx := context.Background()
	workspace := "/workspace/my-app"
	loopback := containerPortMapping{PublicPort: 3000, LoopbackOnly: true}
	wildcard := containerPortMapping{PublicPort: 3000}

	f := &provenanceFixture{containers: []dockerContainerInfo{workspaceContainer(workspace, loopback)}}
	owned, evidence := f.resolver(t.Context()).IsSessionOwned(ctx, "chat-1", workspace, 3000)
	if !owned || evidence.Method != ProvenanceContainer || evidence.OwnerID != "app-web-1" {
		t.Fatalf("loopback workspace container: owned=%v evidence=%+v", owned, evidence)
	}

	f = &provenanceFixture{containers: []dockerContainerInfo{workspaceContainer(workspace, loopback, wildcard)}}
	owned, evidence = f.resolver(t.Context()).IsSessionOwned(ctx, "chat-1", workspace, 3000)
	if owned || evidence.Method != ProvenanceExposed {
		t.Fatalf("container also publishing on every interface: owned=%v evidence=%+v", owned, evidence)
	}

	f = &provenanceFixture{containers: []dockerContainerInfo{workspaceContainer("/workspace/other", loopback)}}
	if owned, _ = f.resolver(t.Context()).IsSessionOwned(ctx, "chat-1", workspace, 3000); owned {
		t.Fatal("another workspace's container was attributed to this one")
	}
}

func TestLoopbackOwnershipPreExistingPortIsForeign(t *testing.T) {
	workspace := "/workspace/my-app"
	f := &provenanceFixture{
		atStart:    []socketListener{listenerAt(555, "127.0.0.1:3000")},
		containers: []dockerContainerInfo{workspaceContainer(workspace, containerPortMapping{PublicPort: 3000, LoopbackOnly: true})},
	}
	owned, evidence := f.resolver(t.Context()).IsSessionOwned(context.Background(), "chat-1", workspace, 3000)
	if owned || evidence.Method != ProvenancePreExisting {
		t.Fatalf("port listening at start: owned=%v evidence=%+v", owned, evidence)
	}
}

// When the start snapshot failed, a port's absence from it proves nothing.
func TestLoopbackOwnershipUnknownBaselineNeverCountsAbsence(t *testing.T) {
	workspace := "/workspace/my-app"
	f := &provenanceFixture{
		startErr:   errors.New("socket table unavailable"),
		now:        []socketListener{listenerAt(4242, "127.0.0.1:8000")},
		sessions:   map[int]string{4242: "chat-1"},
		containers: []dockerContainerInfo{workspaceContainer(workspace, containerPortMapping{PublicPort: 3000, LoopbackOnly: true})},
	}
	p := f.resolver(t.Context())
	owned, evidence := p.IsSessionOwned(context.Background(), "chat-1", workspace, 3000)
	if owned || evidence.Method != ProvenanceUnverified {
		t.Fatalf("container port under an unknown baseline: owned=%v evidence=%+v", owned, evidence)
	}
	if owned, _ := p.IsSessionOwned(context.Background(), "chat-1", workspace, 8000); !owned {
		t.Fatal("lineage ownership does not depend on the start snapshot")
	}
}

func TestHeldByOtherSeparatesFreeOwnedAndForeignPorts(t *testing.T) {
	ctx := context.Background()
	f := &provenanceFixture{
		now:      []socketListener{listenerAt(4242, "127.0.0.1:8000"), listenerAt(900, "127.0.0.1:6379")},
		sessions: map[int]string{4242: "chat-1"},
	}
	p := f.resolver(t.Context())
	for port, want := range map[uint16]bool{8123: false, 8000: false, 6379: true} {
		if got := p.HeldByOther(ctx, "chat-1", "/workspace", port); got != want {
			t.Errorf("HeldByOther(%d) = %v, want %v", port, got, want)
		}
	}
	f.nowErr = errors.New("socket table unavailable")
	if !p.HeldByOther(ctx, "chat-1", "/workspace", 8123) {
		t.Error("an unreadable socket table must not report a port free")
	}
}

// lsof emits a process record followed by that process's files; each name
// belongs to the process record before it.
func TestParseLsofListenersPairsNamesWithTheirProcess(t *testing.T) {
	out := []byte("p100\nf5\nn127.0.0.1:3000\np200\nf7\nn*:3000\nf8\nn[::1]:3001\n")
	got := parseLsofListeners(out)
	want := []socketListener{
		listenerAt(100, "127.0.0.1:3000"),
		{PID: 200, Port: 3000, Addr: netip.IPv6Unspecified()},
		listenerAt(200, "[::1]:3001"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseLsofListeners = %+v, want %+v", got, want)
	}
	if got[1].loopbackBound() {
		t.Fatal("a wildcard bind must not read as loopback")
	}
}

func TestHostListenersSeesALiveListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	testutil.FailErr(t, "listen", err)
	defer ln.Close()
	port := uint16(ln.Addr().(*net.TCPAddr).Port)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	all, err := hostListeners(ctx)
	if err != nil {
		t.Skipf("host socket table unavailable: %v", err)
	}
	if len(onPort(all, port)) == 0 {
		t.Fatalf("listener on %d missing from the host table", port)
	}
}

// loopbackHelperEnv makes the test binary act as a server a chat launched.
const loopbackHelperEnv = "LYCAON_TEST_LOOPBACK_LISTENER"

func TestLoopbackListenerHelper(t *testing.T) {
	if os.Getenv(loopbackHelperEnv) != "1" {
		t.Skip("helper process only")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	testutil.FailErr(t, "listen", err)
	fmt.Println(ln.Addr().(*net.TCPAddr).Port)
	time.Sleep(30 * time.Second)
}

// A server a session's action launched is that session's, through the same
// lineage the egress broker attributes by, and nobody else's.
func TestLoopbackOwnershipFollowsARealLaunch(t *testing.T) {
	if !lineage.Supported() {
		t.Skip("launch lineage requires a supported platform")
	}
	line, err := lineage.Open("command running dev server", "chat-live")
	testutil.FailErr(t, "open lineage", err)
	t.Cleanup(func() { _ = line.Close() })
	cmd := osexec.Command(os.Args[0], "-test.run=^TestLoopbackListenerHelper$")
	cmd.Env = append(os.Environ(), loopbackHelperEnv+"=1")
	cmd.ExtraFiles = []*os.File{line.ChildFile()}
	stdout, err := cmd.StdoutPipe()
	testutil.FailErr(t, "helper stdout", err)
	testutil.FailErr(t, "start helper", cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	line.Started()
	var port uint64
	lines := bufio.NewScanner(stdout)
	for port == 0 && lines.Scan() {
		port, _ = strconv.ParseUint(strings.TrimSpace(lines.Text()), 10, 16)
	}
	if port == 0 {
		t.Fatalf("helper never reported its port: %v", lines.Err())
	}

	p := newLoopbackProvenance(t.Context(), hostListeners, lineageSessionOf, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if owned, evidence := p.IsSessionOwned(ctx, "chat-live", "", uint16(port)); !owned {
		t.Fatalf("server launched by the session was not attributed to it: %+v", evidence)
	}
	if owned, _ := p.IsSessionOwned(ctx, "chat-other", "", uint16(port)); owned {
		t.Fatal("server launched by one session was attributed to another")
	}
}

func TestLoopbackProvenanceDockerContextDiscovery(t *testing.T) {
	tempHome := t.TempDir()
	dockerDir := filepath.Join(tempHome, ".docker")
	contextName := "test-context"
	sum := sha256.Sum256([]byte(contextName))
	metaDir := filepath.Join(dockerDir, "contexts", "meta", hex.EncodeToString(sum[:]))
	testutil.FailErr(t, "create context metadata dir", os.MkdirAll(metaDir, 0o755))

	expectedSocket := filepath.Join(tempHome, "test-context.sock")
	metaJSON := fmt.Sprintf(`{"Name":%q,"Endpoints":{"docker":{"Host":%q}}}`, contextName, "unix://"+expectedSocket)
	testutil.FailErr(t, "write context metadata", os.WriteFile(filepath.Join(metaDir, "meta.json"), []byte(metaJSON), 0o644))
	configJSON := fmt.Sprintf(`{"currentContext":%q}`, contextName)
	testutil.FailErr(t, "write docker config", os.WriteFile(filepath.Join(dockerDir, "config.json"), []byte(configJSON), 0o644))

	if resolved := resolveDockerContextSocket(tempHome); resolved != expectedSocket {
		t.Fatalf("expected resolved socket %q, got %q", expectedSocket, resolved)
	}
}

func TestQueryDockerContainersOverSocket(t *testing.T) {
	f, err := os.CreateTemp("/tmp", "d-*.sock")
	testutil.FailErr(t, "create temp sock path", err)
	sockPath := f.Name()
	_ = f.Close()
	_ = os.Remove(sockPath)
	defer func() { _ = os.Remove(sockPath) }()

	ln, err := net.Listen("unix", sockPath)
	testutil.FailErr(t, "listen on unix socket", err)
	defer ln.Close()

	mockDockerResponse := `[
		{
			"Names": ["/test-web-1"],
			"Ports": [
				{"IP": "127.0.0.1", "PublicPort": 8080},
				{"IP": "0.0.0.0", "PublicPort": 8081},
				{"IP": "::", "PublicPort": 8081},
				{"IP": "::1", "PublicPort": 8082}
			],
			"Labels": {
				"com.docker.compose.project.working_dir": "/workspace/test-proj",
				"com.docker.compose.project": "test-proj"
			}
		}
	]`
	srv := &http.Server{
		ReadHeaderTimeout: time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1.41/containers/json" {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(mockDockerResponse))
				return
			}
			http.NotFound(w, r)
		}),
	}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	containers, err := queryDockerContainersOverSocket(ctx, sockPath)
	testutil.FailErr(t, "query containers", err)
	if len(containers) != 1 {
		t.Fatalf("expected 1 container, got %d", len(containers))
	}
	c := containers[0]
	if c.Name != "test-web-1" || c.WorkingDir != "/workspace/test-proj" {
		t.Fatalf("container identity = %+v", c)
	}
	loopback := map[uint16][]bool{}
	for _, m := range c.Ports {
		loopback[m.PublicPort] = append(loopback[m.PublicPort], m.LoopbackOnly)
	}
	want := map[uint16][]bool{8080: {true}, 8081: {false, false}, 8082: {true}}
	if !reflect.DeepEqual(loopback, want) {
		t.Fatalf("published loopback facts = %v, want %v", loopback, want)
	}
}

func TestStdoutContainerIDAttributesPublishedPort(t *testing.T) {
	containerID := "11223344556677889900aabbccddeeff11223344556677889900aabbccddeeff"
	fixture := &provenanceFixture{
		containers: []dockerContainerInfo{
			{
				ID:   containerID,
				Name: "my-container",
				Ports: []containerPortMapping{
					{PublicPort: 9000, LoopbackOnly: true},
				},
			},
		},
	}
	res := fixture.resolver(t.Context())
	owned, _ := res.IsSessionOwned(context.Background(), "session-1", "/workspace", 9000)
	if owned {
		t.Fatal("port should not be chat-owned before container is recorded")
	}

	res.RecordSessionContainer("session-1", containerID, "/var/run/docker.sock")

	owned, ev := res.IsSessionOwned(context.Background(), "session-1", "/workspace", 9000)
	if !owned {
		t.Fatal("port should be chat-owned after container ID is recorded from stdout")
	}
	if ev.Method != ProvenanceContainer || ev.OwnerID != "my-container" {
		t.Fatalf("unexpected evidence: %+v", ev)
	}

	res.ForgetSession("session-1")
	owned, _ = res.IsSessionOwned(context.Background(), "session-1", "/workspace", 9000)
	if owned {
		t.Fatal("port should not be chat-owned after session is forgotten")
	}
}
