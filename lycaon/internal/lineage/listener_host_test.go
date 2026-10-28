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
		if len(got) != 1 || got[0].Protocol != tc.protocol || got[0].Addr != want || got[0].UID != uint32(os.Getuid()) || !slices.Contains(got[0].PIDs, os.Getpid()) {
			t.Fatalf("%s listener on %d = %+v, want this process at %s", tc.protocol, tc.port, got, want)
		}
	}
	if got, err := lineage.Listeners(lineage.ListenerQuery{Protocols: []lineage.Protocol{lineage.ProtocolUDP}, Port: tcpPort}); err != nil || len(got) != 0 {
		t.Fatalf("udp query returned the tcp listener: %+v, %v", got, err)
	}
}
