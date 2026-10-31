//go:build linux || (darwin && cgo)

package lineage_test

import (
	"net"
	"net/netip"
	"os"
	"slices"
	"syscall"
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

func TestListenersReportsIPv6OnlyFlagAndKeepsFamiliesSeparate(t *testing.T) {
	listenV6 := func(t *testing.T, v6Only int) net.Listener {
		t.Helper()
		lc := net.ListenConfig{
			Control: func(_, _ string, c syscall.RawConn) error {
				var sockErr error
				if err := c.Control(func(fd uintptr) {
					sockErr = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IPV6, syscall.IPV6_V6ONLY, v6Only)
				}); err != nil {
					return err
				}
				return sockErr
			},
		}
		ln, err := lc.Listen(t.Context(), "tcp6", "[::]:0")
		if err != nil {
			t.Skipf("tcp6 listen unavailable: %v", err)
		}
		t.Cleanup(func() { _ = ln.Close() })
		return ln
	}

	dual := listenV6(t, 0)
	dualPort := uint16(dual.Addr().(*net.TCPAddr).Port)
	gotDual, err := lineage.Listeners(lineage.ListenerQuery{Protocols: []lineage.Protocol{lineage.ProtocolTCP}, Port: dualPort})
	testutil.FailErr(t, "dual-stack listeners", err)
	wantDualAddr := netip.AddrPortFrom(netip.IPv6Unspecified(), dualPort)
	if !slices.ContainsFunc(gotDual, func(l lineage.Listener) bool {
		return l.Addr == wantDualAddr && !l.IPv6Only
	}) {
		t.Fatalf("dual-stack listener on %d = %+v, want [::]:%d with IPv6Only=false", dualPort, gotDual, dualPort)
	}

	v6OnlyLn := listenV6(t, 1)
	v6Port := uint16(v6OnlyLn.Addr().(*net.TCPAddr).Port)
	v4Ln, err := net.Listen("tcp4", netip.AddrPortFrom(netip.IPv4Unspecified(), v6Port).String())
	testutil.FailErr(t, "listen tcp4 on same port as v6only", err)
	defer v4Ln.Close()

	gotBoth, err := lineage.Listeners(lineage.ListenerQuery{Protocols: []lineage.Protocol{lineage.ProtocolTCP}, Port: v6Port})
	testutil.FailErr(t, "both-family listeners", err)
	wantV6Addr := netip.AddrPortFrom(netip.IPv6Unspecified(), v6Port)
	wantV4Addr := netip.AddrPortFrom(netip.IPv4Unspecified(), v6Port)
	if !slices.ContainsFunc(gotBoth, func(l lineage.Listener) bool {
		return l.Addr == wantV6Addr && l.IPv6Only
	}) {
		t.Fatalf("listeners on %d = %+v, want [::]:%d with IPv6Only=true", v6Port, gotBoth, v6Port)
	}
	if !slices.ContainsFunc(gotBoth, func(l lineage.Listener) bool {
		return l.Addr == wantV4Addr && !l.IPv6Only
	}) {
		t.Fatalf("listeners on %d = %+v, want 0.0.0.0:%d kept separate with IPv6Only=false", v6Port, gotBoth, v6Port)
	}
}
