//go:build darwin && cgo

package lineage

import (
	"net"
	"net/netip"
	"os"
	"os/exec"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestBindExcludesOthersRequiresAnUnsharedHolderAndAProvenSibling(t *testing.T) {
	endpoint := netip.MustParseAddrPort("127.0.0.1:8123")
	exclusive, shared := SocketObservation{}, SocketObservation{sharedBind: true}
	for _, tc := range []struct {
		name   string
		walk   endpointHolders
		free   bool
		probes int
		want   bool
	}{
		{name: "unshared holder and a free sibling", walk: endpointHolders{holders: []SocketObservation{exclusive}}, free: true, probes: 1, want: true},
		{name: "something on the sibling", walk: endpointHolders{holders: []SocketObservation{exclusive}}, probes: 1},
		{name: "the session holds the sibling", walk: endpointHolders{holders: []SocketObservation{exclusive}, sibling: []SocketObservation{exclusive}}, want: true},
		{name: "a holder that admits a second bind", walk: endpointHolders{holders: []SocketObservation{exclusive, shared}}, free: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			probes := 0
			got := bindExcludesOthers(endpoint, tc.walk, func(probed netip.AddrPort) bool {
				probes++
				if probed != netip.MustParseAddrPort("[::1]:8123") {
					t.Fatalf("probed %s", probed)
				}
				return tc.free
			})
			if got != tc.want || probes != tc.probes {
				t.Fatalf("excluded = %v after %d probes", got, probes)
			}
		})
	}
}

// The native walk reports the option the exclusion rests on.
func TestSocketsOfReportsASharedBind(t *testing.T) {
	plain, err := net.Listen("tcp", "127.0.0.1:0")
	testutil.FailErr(t, "listen", err)
	defer plain.Close()
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, 0)
	testutil.FailErr(t, "socket", err)
	defer syscall.Close(fd)
	testutil.FailErr(t, "set reuse port", syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_REUSEPORT, 1))
	testutil.FailErr(t, "bind", syscall.Bind(fd, &syscall.SockaddrInet4{Addr: [4]byte{127, 0, 0, 1}}))
	testutil.FailErr(t, "listen shared", syscall.Listen(fd, 1))
	name, err := syscall.Getsockname(fd)
	testutil.FailErr(t, "socket name", err)
	sharedPort := uint16(name.(*syscall.SockaddrInet4).Port)
	sockets, err := SocketsOf(os.Getpid())
	testutil.FailErr(t, "sockets of this process", err)
	plainPort := plain.Addr().(*net.TCPAddr).AddrPort().Port()
	seen := map[uint16]bool{}
	for _, socket := range sockets {
		if !socket.Listening {
			continue
		}
		switch socket.Local.Port() {
		case plainPort:
			seen[plainPort] = true
			if socket.sharedBind {
				t.Errorf("plain listener reported shared")
			}
		case sharedPort:
			seen[sharedPort] = true
			if !socket.sharedBind {
				t.Errorf("reuse-port listener reported unshared")
			}
		}
	}
	if !seen[plainPort] || !seen[sharedPort] {
		t.Fatalf("listeners not observed: %v in %+v", seen, sockets)
	}
}

func TestLoopbackAddressFreeReflectsTheKernel(t *testing.T) {
	for _, network := range []string{"tcp4", "tcp6"} {
		host := "127.0.0.1:0"
		if network == "tcp6" {
			host = "[::1]:0"
		}
		listener, err := net.Listen(network, host)
		testutil.FailErr(t, "listen", err)
		endpoint := listener.Addr().(*net.TCPAddr).AddrPort()
		if loopbackAddressFree(endpoint) {
			t.Errorf("%s reported free while held", endpoint)
		}
		testutil.FailErr(t, "close", listener.Close())
		if !loopbackAddressFree(endpoint) {
			t.Errorf("%s reported held after a listener with no connections closed", endpoint)
		}
	}
}

// Only the kernel's refusal of another user's process leaves a holder unknown.
func TestUnreadableByThisUserRequiresAnotherOwner(t *testing.T) {
	child := exec.Command("/bin/sleep", "60")
	testutil.FailErr(t, "start child", child.Start())
	defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
	if unreadableByThisUser(child.Process.Pid, syscall.EPERM) {
		t.Error("a process of this user was treated as another user's")
	}
	if os.Geteuid() != 0 {
		if !unreadableByThisUser(1, syscall.EPERM) {
			t.Error("launchd was not treated as another user's")
		}
		if unreadableByThisUser(1, syscall.EIO) {
			t.Error("a failure other than the kernel's refusal was treated as one")
		}
		if _, err := SocketsOf(1); !unreadableByThisUser(1, err) {
			t.Errorf("reading launchd's sockets failed with %v", err)
		}
	}
}
