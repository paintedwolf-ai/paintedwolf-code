package lineage

import (
	"net"
	"net/netip"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

// Peer resolution reads kernel socket tables whose returned sizes are not
// guaranteed to match the declared structs. This pins the behaviour the broker
// depends on for every attribution.
func TestPeerResolutionFindsTheProcessOnTheOtherEnd(t *testing.T) {
	if port := os.Getenv("LINEAGE_PEER_HELPER_PORT"); port != "" {
		// The test binary itself is the peer, so no shell feature is assumed.
		conn, err := net.Dial("tcp", "127.0.0.1:"+port)
		if err != nil {
			os.Exit(1)
		}
		time.Sleep(5 * time.Second)
		_ = conn.Close()
		os.Exit(0)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	testutil.FailErr(t, "net.Listen failed", err)
	defer ln.Close()
	testutil.FailErr(t, "set accept deadline", ln.(*net.TCPListener).SetDeadline(time.Now().Add(10*time.Second)))
	addr := ln.Addr().(*net.TCPAddr)
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestPeerResolutionFindsTheProcessOnTheOtherEnd$")
	cmd.Env = append(os.Environ(), "LINEAGE_PEER_HELPER_PORT="+strconv.Itoa(addr.Port))
	testutil.FailErr(t, "start the peer", cmd.Start())
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	conn, err := ln.Accept()
	testutil.FailErr(t, "ln.Accept failed", err)
	defer conn.Close()
	time.Sleep(100 * time.Millisecond)
	local, _ := netip.ParseAddrPort(conn.LocalAddr().String())
	remote, _ := netip.ParseAddrPort(conn.RemoteAddr().String())
	if _, ok := peerPID(local, remote); !ok {
		t.Fatal("the process on the other end of a loopback connection was not resolved")
	}
}
