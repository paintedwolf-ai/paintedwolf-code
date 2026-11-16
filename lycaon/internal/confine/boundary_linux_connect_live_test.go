//go:build linux

package confine_test

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/testutil"
)

// hostConnectProbe connects to each target with a raw socket so the probe
// sees the connect's own errno: "tcpstart" from a non-blocking socket,
// "tcpblocking" from a blocking one that then reads the greeting.
func hostConnectProbe(mode string) int {
	for _, target := range strings.Split(os.Getenv(linuxProbePathsEnv), "\n") {
		ap := netip.MustParseAddrPort(target)
		flags := unix.SOCK_STREAM | unix.SOCK_CLOEXEC
		if mode == "tcpstart" {
			flags |= unix.SOCK_NONBLOCK
		}
		fd, err := unix.Socket(unix.AF_INET, flags, 0)
		if err != nil {
			fmt.Println("socket:", err)
			return 2
		}
		err = unix.Connect(fd, &unix.SockaddrInet4{Addr: ap.Addr().As4(), Port: int(ap.Port())})
		switch {
		case mode == "tcpstart" && errors.Is(err, unix.EINPROGRESS):
			fmt.Printf("connect %s: EINPROGRESS\n", target)
		case errors.Is(err, unix.ECONNREFUSED):
			fmt.Printf("connect %s: ECONNREFUSED\n", target)
		case err != nil:
			fmt.Printf("connect %s: %s\n", target, errnoName(err))
		default:
			greeting := make([]byte, 64)
			n, _ := unix.Read(fd, greeting)
			fmt.Printf("connect %s: ok %s\n", target, greeting[:max(n, 0)])
		}
		_ = unix.Close(fd)
	}
	return 0
}

// stalledListener listens with a zero backlog and fills its accept queue, so
// the kernel drops every further SYN and a connect to it never completes.
func stalledListener(t *testing.T) string {
	t.Helper()
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	testutil.FailErr(t, "socket", err)
	t.Cleanup(func() { _ = unix.Close(fd) })
	testutil.FailErr(t, "bind", unix.Bind(fd, &unix.SockaddrInet4{Addr: [4]byte{127, 0, 0, 1}}))
	testutil.FailErr(t, "listen", unix.Listen(fd, 0))
	sa, err := unix.Getsockname(fd)
	testutil.FailErr(t, "getsockname", err)
	addr := netip.AddrPortFrom(netip.AddrFrom4([4]byte{127, 0, 0, 1}), uint16(sa.(*unix.SockaddrInet4).Port)).String()
	queued, err := net.Dial("tcp", addr)
	testutil.FailErr(t, "fill the accept queue", err)
	t.Cleanup(func() { _ = queued.Close() })
	return addr
}

// closedPort names a loopback port nothing listens on.
func closedPort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	testutil.FailErr(t, "listen", err)
	addr := ln.Addr().String()
	testutil.FailErr(t, "close", ln.Close())
	return addr
}

func addrPort(t *testing.T, addr string) uint16 {
	t.Helper()
	ap, err := netip.ParseAddrPort(addr)
	testutil.FailErr(t, "parse "+addr, err)
	return ap.Port()
}

// An admitted stream connect runs on a host socket the supervisor connects
// for the caller. A non-blocking caller gets EINPROGRESS at once, as it would
// unconfined, even toward a peer that never answers: the supervisor does not
// wait out the kernel's handshake on the caller's behalf.
func TestLinuxAdmittedNonblockingConnectReturnsInProgress(t *testing.T) {
	self := requireLinuxSandbox(t)
	stalled, open := stalledListener(t), greeter(t, "tcp", "127.0.0.1:0").Addr().String()
	c, _ := boundConfinement(t, confine.Confinement{
		Roots: sandboxRoots(t.TempDir()), Network: confine.NetworkDeny, EgressOff: confine.EgressOffOverride,
		LoopbackConnect: true, LoopbackConnectPorts: []uint16{addrPort(t, stalled), addrPort(t, open)},
	})
	out := probeLines(t, self, *c, "tcpstart", stalled, open)
	for _, addr := range []string{stalled, open} {
		if !strings.Contains(out, "connect "+addr+": EINPROGRESS") {
			t.Fatalf("a non-blocking connect to %s did not return EINPROGRESS:\n%s", addr, out)
		}
	}
}

// A blocking caller sees the handshake's outcome from its connect call.
func TestLinuxAdmittedBlockingConnectReportsTheHandshake(t *testing.T) {
	self := requireLinuxSandbox(t)
	open, refused := greeter(t, "tcp", "127.0.0.1:0").Addr().String(), closedPort(t)
	c, _ := boundConfinement(t, confine.Confinement{
		Roots: sandboxRoots(t.TempDir()), Network: confine.NetworkDeny, EgressOff: confine.EgressOffOverride,
		LoopbackConnect: true, LoopbackConnectPorts: []uint16{addrPort(t, open), addrPort(t, refused)},
	})
	out := probeLines(t, self, *c, "tcpblocking", open, refused)
	if !strings.Contains(out, "connect "+open+": ok host") {
		t.Fatalf("a blocking connect did not reach the host listener:\n%s", out)
	}
	if !strings.Contains(out, "connect "+refused+": ECONNREFUSED") {
		t.Fatalf("a blocking connect to a closed port did not report ECONNREFUSED:\n%s", out)
	}
}
