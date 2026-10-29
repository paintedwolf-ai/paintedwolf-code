//go:build (darwin && cgo) || linux

package hostprocess

import (
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"testing"

	"github.com/lycaon/lycaon/internal/lineage"
	"github.com/lycaon/lycaon/internal/testutil"
)

// startHolder starts /bin/sleep holding a real listening socket the test then
// closes, so the child is its only holder. A non-nil lineage makes the child
// that lineage's descendant the way a confined launch does.
func startHolder(t *testing.T, line *lineage.Lineage) (*exec.Cmd, int) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	testutil.FailErr(t, "listen", err)
	file, err := listener.(*net.TCPListener).File()
	testutil.FailErr(t, "listener file", err)
	child := exec.Command("/bin/sleep", "60")
	child.ExtraFiles = []*os.File{file}
	if line != nil {
		child.ExtraFiles = append(child.ExtraFiles, line.ChildFile())
	}
	testutil.FailErr(t, "start holder", child.Start())
	if line != nil {
		line.Started()
	}
	port := listener.Addr().(*net.TCPAddr).Port
	testutil.FailErr(t, "close parent file", file.Close())
	testutil.FailErr(t, "close parent listener", listener.Close())
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	return child, port
}

// A real listener another process holds is named with its PID and command;
// the session's own holder carries the reference process_signal needs, and a
// holder outside the session never does.
func TestPortHoldersNamesRealHolders(t *testing.T) {
	service, err := New()
	testutil.FailErr(t, "create service", err)
	line, err := lineage.Open("command:call_1", "task")
	testutil.FailErr(t, "open lineage", err)
	t.Cleanup(func() { _ = line.Close() })
	owned, ownedPort := startHolder(t, line)
	foreign, foreignPort := startHolder(t, nil)
	for _, tc := range []struct {
		port  int
		child *exec.Cmd
		owned bool
	}{{ownedPort, owned, true}, {foreignPort, foreign, false}} {
		holders, err := service.PortHolders(t.Context(), "task", lineage.ListenerQuery{Protocols: []lineage.Protocol{lineage.ProtocolTCP}, Port: uint16(tc.port)})
		testutil.FailErr(t, "port holders", err)
		if len(holders) != 1 || holders[0].Process == nil {
			t.Fatalf("holders of %d = %+v", tc.port, holders)
		}
		got := holders[0]
		want := ListeningSocket{Protocol: lineage.ProtocolTCP, Address: netip.MustParseAddr("127.0.0.1"), Port: tc.port, UID: uint32(os.Getuid())}
		if got.Socket != want || got.Process.PID != tc.child.Process.Pid || got.Process.Name != filepath.Base(tc.child.Path) {
			t.Fatalf("holder of %d = %+v %+v, want %+v held by %d", tc.port, got.Socket, got.Process, want, tc.child.Process.Pid)
		}
		if got.Process.Owned != tc.owned || (got.Process.Reference != "") != tc.owned {
			t.Fatalf("holder of %d owned=%v reference=%q, want owned=%v", tc.port, got.Process.Owned, got.Process.Reference, tc.owned)
		}
		if tc.owned {
			resolved, err := service.Resolve("task", got.Process.Reference)
			if err != nil || resolved.PID != tc.child.Process.Pid {
				t.Fatalf("owned holder reference resolves to %+v, %v", resolved, err)
			}
		}
	}
}

// The port lookup reports the holder the shared native socket observation
// sees for the socket: same PID and the same process incarnation.
func TestPortHoldersReportsTheObservedHolder(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	testutil.FailErr(t, "listen", err)
	defer listener.Close()
	endpoint := listener.Addr().(*net.TCPAddr).AddrPort()
	sockets, err := lineage.SocketsOf(os.Getpid())
	testutil.FailErr(t, "observe this process", err)
	var observed lineage.ProcessIdentity
	for _, socket := range sockets {
		if socket.Listening && socket.Local == endpoint {
			observed = socket.Holder
		}
	}
	if observed.PID != os.Getpid() {
		t.Fatalf("native observation of %s = %+v", endpoint, sockets)
	}
	service, err := New()
	testutil.FailErr(t, "create service", err)
	holders, err := service.PortHolders(t.Context(), "task", lineage.ListenerQuery{Protocols: []lineage.Protocol{lineage.ProtocolTCP}, Port: endpoint.Port()})
	testutil.FailErr(t, "port holders", err)
	if len(holders) != 1 || holders[0].Process == nil || holders[0].Process.PID != observed.PID || holders[0].Process.StartOrder != uint64(observed.Started) {
		t.Fatalf("port holders of %s = %+v, want the observed holder %+v", endpoint, holders, observed)
	}
}

// incarnation reads the identity the shared socket observation reports for pid.
func incarnation(t *testing.T, pid int) lineage.ProcessIdentity {
	t.Helper()
	process, err := inspect(pid)
	testutil.FailErr(t, "inspect holder", err)
	return lineage.ProcessIdentity{PID: pid, Started: int64(process.StartOrder)}
}

// fixtureService answers listener queries from a fixed table and session set.
func fixtureService(t *testing.T, table []lineage.Listener, members map[int]struct{}) *Service {
	t.Helper()
	service, err := New()
	testutil.FailErr(t, "create service", err)
	service.members = func(string) (map[int]struct{}, error) { return members, nil }
	service.listeners = func(query lineage.ListenerQuery) ([]lineage.Listener, error) {
		var out []lineage.Listener
		for _, l := range table {
			if (query.Port == 0 || l.Addr.Port() == query.Port) && (len(query.Protocols) == 0 || slices.Contains(query.Protocols, l.Protocol)) {
				out = append(out, l)
			}
		}
		return out, nil
	}
	return service
}

// A socket whose holder cannot be read keeps its port with the PID unknown,
// and so does one whose holder exited, or whose PID now names another process
// incarnation, between the socket observation and inspection.
func TestPortHoldersKeepsUnreadableHoldersAsUnknown(t *testing.T) {
	// No platform assigns a PID this large, so inspecting it always fails.
	const gone = 1 << 30
	self := os.Getpid()
	current := incarnation(t, self)
	reused := current
	reused.Started++
	service := fixtureService(t, []lineage.Listener{
		{Protocol: lineage.ProtocolUDP, Addr: netip.MustParseAddrPort("[::]:5353"), UID: 0, Holders: []lineage.ProcessIdentity{current}},
		{Protocol: lineage.ProtocolTCP, Addr: netip.MustParseAddrPort("[::ffff:127.0.0.1]:5432"), UID: 70},
		{Protocol: lineage.ProtocolTCP, Addr: netip.MustParseAddrPort("0.0.0.0:5432"), UID: 70, Holders: []lineage.ProcessIdentity{{PID: gone, Started: 1}}},
		{Protocol: lineage.ProtocolTCP, Addr: netip.MustParseAddrPort("0.0.0.0:3000"), UID: 501, Holders: []lineage.ProcessIdentity{current}},
		{Protocol: lineage.ProtocolTCP, Addr: netip.MustParseAddrPort("127.0.0.1:6000"), UID: 501, Holders: []lineage.ProcessIdentity{reused}},
	}, map[int]struct{}{self: {}})
	holders, err := service.PortHolders(t.Context(), "task", lineage.ListenerQuery{})
	testutil.FailErr(t, "port holders", err)
	var got []string
	for _, h := range holders {
		pid := 0
		if h.Process != nil {
			pid = h.Process.PID
			if !h.Process.Owned || h.Process.Reference == "" {
				t.Fatalf("session holder lost its identity: %+v", h.Process)
			}
		}
		got = append(got, string(h.Socket.Protocol)+" "+netip.AddrPortFrom(h.Socket.Address, uint16(h.Socket.Port)).String()+" "+strconv.Itoa(pid))
	}
	want := []string{"tcp 0.0.0.0:3000 " + strconv.Itoa(self), "udp [::]:5353 " + strconv.Itoa(self), "tcp 0.0.0.0:5432 0", "tcp 127.0.0.1:5432 0", "tcp 127.0.0.1:6000 0"}
	if !slices.Equal(got, want) {
		t.Fatalf("holders = %q, want %q", got, want)
	}
}

func TestListeningPagesHoldersWithTheirSockets(t *testing.T) {
	self := os.Getpid()
	child := exec.Command("/bin/sleep", "60")
	testutil.FailErr(t, "start child", child.Start())
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	first, second := min(self, child.Process.Pid), max(self, child.Process.Pid)
	firstHolder, secondHolder := incarnation(t, first), incarnation(t, second)
	service := fixtureService(t, []lineage.Listener{
		{Protocol: lineage.ProtocolTCP, Addr: netip.MustParseAddrPort("127.0.0.1:8080"), UID: 501, Holders: []lineage.ProcessIdentity{secondHolder}},
		{Protocol: lineage.ProtocolTCP, Addr: netip.MustParseAddrPort("[::1]:8080"), UID: 501, Holders: []lineage.ProcessIdentity{firstHolder, secondHolder}},
		{Protocol: lineage.ProtocolTCP, Addr: netip.MustParseAddrPort("0.0.0.0:8080"), UID: 0},
	}, nil)
	query := lineage.ListenerQuery{Port: 8080}
	page, err := service.Listening(t.Context(), "task", query, 0, 0, 1)
	testutil.FailErr(t, "first page", err)
	if len(page.Processes) != 1 || page.Processes[0].PID != first || len(page.Processes[0].Listeners) != 1 || page.NextAfterPID != first || len(page.Unattributed) != 1 || page.Unattributed[0].UID != 0 {
		t.Fatalf("first page = %+v", page)
	}
	page, err = service.Listening(t.Context(), "task", query, 0, page.NextAfterPID, 10)
	testutil.FailErr(t, "second page", err)
	if len(page.Processes) != 1 || page.Processes[0].PID != second || len(page.Processes[0].Listeners) != 2 || page.NextAfterPID != 0 || len(page.Unattributed) != 0 {
		t.Fatalf("second page = %+v", page)
	}
	page, err = service.Listening(t.Context(), "task", query, first, 0, 10)
	testutil.FailErr(t, "one pid", err)
	if len(page.Processes) != 1 || page.Processes[0].PID != first || len(page.Unattributed) != 0 {
		t.Fatalf("pid page = %+v", page)
	}
	if page.Processes[0].Owned || page.Processes[0].Reference != "" {
		t.Fatalf("a holder outside the session carries a reference: %+v", page.Processes[0])
	}
}
