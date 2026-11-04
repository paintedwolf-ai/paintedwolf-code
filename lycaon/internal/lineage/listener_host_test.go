//go:build linux || (darwin && cgo)

package lineage_test

import (
	"bufio"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/lineage"
	"github.com/lycaon/lycaon/internal/testutil"
)

// The kernel tables name this process as the holder of real TCP and UDP
// listeners, and a query for one protocol never returns the other.
func TestListenersAttributesRealSockets(t *testing.T) {
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	testutil.FailErr(t, "listen tcp", err)
	defer tcp.Close()
	udp, err := net.ListenPacket("udp", "127.0.0.1:0")
	testutil.FailErr(t, "listen udp", err)
	defer udp.Close()
	tcpPort := uint16(tcp.Addr().(*net.TCPAddr).Port)
	udpPort := uint16(udp.LocalAddr().(*net.UDPAddr).Port)
	for _, tc := range []struct {
		protocol lineage.Protocol
		port     uint16
	}{{lineage.ProtocolTCP, tcpPort}, {lineage.ProtocolUDP, udpPort}} {
		table, err := lineage.Listeners(lineage.ListenerQuery{Protocols: []lineage.Protocol{tc.protocol}, Port: tc.port})
		testutil.FailErr(t, "listeners", err)
		got := table.Listeners
		want := netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), tc.port)
		if len(got) != 1 || got[0].Protocol != tc.protocol || got[0].Addr != want || got[0].UID != uint32(os.Getuid()) ||
			!slices.ContainsFunc(got[0].Holders, func(h lineage.ProcessIdentity) bool { return h.PID == os.Getpid() && h.Current() }) {
			t.Fatalf("%s listener on %d = %+v, want this process at %s", tc.protocol, tc.port, got, want)
		}
	}
	if got, err := lineage.Listeners(lineage.ListenerQuery{Protocols: []lineage.Protocol{lineage.ProtocolUDP}, Port: tcpPort}); err != nil || len(got.Listeners) != 0 {
		t.Fatalf("udp query returned the tcp listener: %+v, %v", got, err)
	}
}

// The host listener table names the same holder incarnation the native
// per-process observation reports for that socket, so the port lookup and
// loopback admission read one adapter.
func TestListenersHolderIsTheSocketObservationHolder(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	testutil.FailErr(t, "listen", err)
	defer listener.Close()
	endpoint := listener.Addr().(*net.TCPAddr).AddrPort()
	sockets, err := lineage.SocketsOf(os.Getpid())
	testutil.FailErr(t, "sockets of this process", err)
	var observed *lineage.SocketObservation
	for i := range sockets {
		if sockets[i].Listening && sockets[i].Local == endpoint {
			observed = &sockets[i]
		}
	}
	if observed == nil || observed.Protocol != lineage.ProtocolTCP {
		t.Fatalf("native observation of %s = %+v", endpoint, sockets)
	}
	table, err := lineage.Listeners(lineage.ListenerQuery{Protocols: []lineage.Protocol{lineage.ProtocolTCP}, Port: endpoint.Port()})
	testutil.FailErr(t, "listeners", err)
	got := table.Listeners
	if len(got) != 1 || !slices.Equal(got[0].Holders, []lineage.ProcessIdentity{observed.Holder}) {
		t.Fatalf("listener table holders %+v, want the observed holder %+v", got, observed.Holder)
	}
}

const listenerChildEnv = "PW_LISTENER_TABLE_CHILD"

func TestListenerTableChild(t *testing.T) {
	if os.Getenv(listenerChildEnv) != "1" {
		t.Skip("helper process only")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	testutil.FailErr(t, "listen", err)
	defer listener.Close()
	if _, err := os.Stdout.WriteString(strconv.Itoa(listener.Addr().(*net.TCPAddr).Port) + "\n"); err != nil {
		testutil.FailErr(t, "report port", err)
	}
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
}

// Another process of this user is always reported with its holder, whether the
// host-wide table delivers that row or only the process walk can see it.
func TestListenersReportAnotherProcessOfThisUser(t *testing.T) {
	child := exec.Command(os.Args[0], "-test.run=^TestListenerTableChild$")
	child.Env = append(os.Environ(), listenerChildEnv+"=1")
	input, err := child.StdinPipe()
	testutil.FailErr(t, "child input", err)
	output, err := child.StdoutPipe()
	testutil.FailErr(t, "child output", err)
	testutil.FailErr(t, "start child", child.Start())
	t.Cleanup(func() { _ = input.Close(); _ = child.Wait() })
	reader := bufio.NewReader(output)
	line, err := reader.ReadString('\n')
	testutil.FailErr(t, "read child port", err)
	port, err := strconv.ParseUint(strings.TrimSpace(line), 10, 16)
	testutil.FailErr(t, "parse child port", err)
	table, err := lineage.Listeners(lineage.ListenerQuery{Protocols: []lineage.Protocol{lineage.ProtocolTCP}, Port: uint16(port)})
	testutil.FailErr(t, "listeners", err)
	if len(table.Listeners) != 1 || table.Listeners[0].UID != uint32(os.Getuid()) ||
		!slices.ContainsFunc(table.Listeners[0].Holders, func(h lineage.ProcessIdentity) bool { return h.PID == child.Process.Pid }) {
		t.Fatalf("listener of port %d = %+v, want held by %d (complete=%v)", port, table.Listeners, child.Process.Pid, table.Complete)
	}
}
