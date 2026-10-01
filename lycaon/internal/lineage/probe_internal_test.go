package lineage

import (
	"net"
	"net/netip"
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
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	testutil.FailErr(t, "net.Listen failed", err)
	defer ln.Close()
	addr := ln.Addr().(*net.TCPAddr)
	cmd := exec.Command("/bin/sh", "-c", "exec 3<>/dev/tcp/127.0.0.1/"+strconv.Itoa(addr.Port)+"; sleep 5")
	if err := cmd.Start(); err != nil {
		t.Skip("shell tcp redirection unavailable")
	}
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
