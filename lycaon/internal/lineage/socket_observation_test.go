package lineage_test

import (
	"net"
	"net/netip"
	"os"
	"testing"

	"github.com/lycaon/lycaon/internal/lineage"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestNativeSocketObservationIdentifiesExactAddressAndLifetime(t *testing.T) {
	if !lineage.Supported() {
		t.Skip("native observation is unavailable")
	}
	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		testutil.FailErr(t, "listen for process-incarnation observation", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	endpoint := listener.Addr().(*net.TCPAddr).AddrPort()
	sockets, err := lineage.SocketsOf(os.Getpid())
	if err != nil {
		testutil.FailErr(t, "observe current process sockets", err)
	}
	var captured lineage.SocketObservation
	for _, socket := range sockets {
		if socket.Listening && socket.Local == endpoint {
			captured = socket
			break
		}
	}
	if captured.ResourceID == "" || captured.Holder.PID != os.Getpid() || !captured.Holder.Current() {
		t.Fatalf("native listener was not identified: %+v", captured)
	}
	stale := captured.Holder
	stale.Started++
	if stale.Current() {
		t.Fatal("different process incarnation accepted")
	}
	if err := listener.Close(); err != nil {
		testutil.FailErr(t, "close observed listener", err)
	}
	// Rebinding the exact endpoint must expose a new kernel resource, even when
	// its holder incarnation and port remain unchanged.
	replacement, err := net.ListenTCP("tcp4", net.TCPAddrFromAddrPort(endpoint))
	if err != nil {
		testutil.FailErr(t, "bind socket replacement", err)
	}
	defer replacement.Close()
	sockets, err = lineage.SocketsOf(os.Getpid())
	if err != nil {
		testutil.FailErr(t, "observe replacement socket identity", err)
	}
	seen := false
	for _, socket := range sockets {
		if socket.Listening && socket.Local == endpoint {
			seen = true
			if socket.ResourceID == captured.ResourceID {
				t.Fatal("closed resource identity reused for new listener")
			}
		}
	}
	if !seen {
		t.Fatal("replacement listener absent")
	}
}

func TestNativeConnectedSocketObservationCarriesExactFourTuple(t *testing.T) {
	if !lineage.Supported() {
		t.Skip("native observation is unavailable")
	}
	listener, err := net.ListenTCP("tcp4", net.TCPAddrFromAddrPort(netip.MustParseAddrPort("127.0.0.1:0")))
	if err != nil {
		testutil.FailErr(t, "listen for connected socket fixture", err)
	}
	defer listener.Close()
	client, err := net.DialTCP("tcp4", nil, listener.Addr().(*net.TCPAddr))
	if err != nil {
		testutil.FailErr(t, "connect fixture client", err)
	}
	defer client.Close()
	server, err := listener.AcceptTCP()
	if err != nil {
		testutil.FailErr(t, "accept fixture server connection", err)
	}
	defer server.Close()
	local, remote := client.LocalAddr().(*net.TCPAddr).AddrPort(), client.RemoteAddr().(*net.TCPAddr).AddrPort()
	sockets, err := lineage.SocketsOf(os.Getpid())
	if err != nil {
		testutil.FailErr(t, "observe connected socket endpoints", err)
	}
	for _, socket := range sockets {
		if !socket.Listening && socket.Local == local && socket.Remote == remote && socket.ResourceID != "" {
			return
		}
	}
	t.Fatalf("connected exact tuple absent: %s -> %s", local, remote)
}
