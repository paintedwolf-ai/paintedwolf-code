//go:build linux || (darwin && cgo)

package lineage_test

import (
	"net"
	"net/netip"
	"os"
	"slices"
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
		got, err := lineage.Listeners(lineage.ListenerQuery{Protocols: []lineage.Protocol{tc.protocol}, Port: tc.port})
		testutil.FailErr(t, "listeners", err)
		want := netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), tc.port)
		if len(got) != 1 || got[0].Protocol != tc.protocol || got[0].Addr != want || got[0].UID != uint32(os.Getuid()) ||
			!slices.ContainsFunc(got[0].Holders, func(h lineage.ProcessIdentity) bool { return h.PID == os.Getpid() && h.Current() }) {
			t.Fatalf("%s listener on %d = %+v, want this process at %s", tc.protocol, tc.port, got, want)
		}
	}
	if got, err := lineage.Listeners(lineage.ListenerQuery{Protocols: []lineage.Protocol{lineage.ProtocolUDP}, Port: tcpPort}); err != nil || len(got) != 0 {
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
	got, err := lineage.Listeners(lineage.ListenerQuery{Protocols: []lineage.Protocol{lineage.ProtocolTCP}, Port: endpoint.Port()})
	testutil.FailErr(t, "listeners", err)
	if len(got) != 1 || !slices.Equal(got[0].Holders, []lineage.ProcessIdentity{observed.Holder}) {
		t.Fatalf("listener table holders %+v, want the observed holder %+v", got, observed.Holder)
	}
}
