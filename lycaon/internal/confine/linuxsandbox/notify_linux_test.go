//go:build linux

package linuxsandbox

import (
	"encoding/binary"
	"errors"
	"testing"

	"golang.org/x/sys/unix"
)

func TestSupervisorRefusesAddressesOfUnisolatedSockets(t *testing.T) {
	t.Parallel()
	vsock := make([]byte, 16)
	binary.NativeEndian.PutUint16(vsock, unix.AF_VSOCK)
	binary.NativeEndian.PutUint32(vsock[4:], 1234)
	binary.NativeEndian.PutUint32(vsock[8:], 2)
	var s supervisor
	for _, op := range []SocketOp{OpConnect, OpBind} {
		req := SocketRequest{Op: op}
		err := s.decodeAddress(&req, socketInfo{domain: unix.AF_VSOCK, typ: unix.SOCK_STREAM}, vsock, nil)
		if !errors.Is(err, errUnisolatedFamily) {
			t.Fatalf("%s vsock address: got %v, want errUnisolatedFamily", op, err)
		}
	}
	netlink := make([]byte, 12)
	binary.NativeEndian.PutUint16(netlink, unix.AF_NETLINK)
	req := SocketRequest{Op: OpBind}
	if err := s.decodeAddress(&req, socketInfo{domain: unix.AF_NETLINK, typ: unix.SOCK_RAW}, netlink, nil); err != nil {
		t.Fatalf("netlink bind: %v", err)
	}
	if req.Unix != "" || req.Abstract || req.IP.IsValid() {
		t.Fatalf("netlink bind decoded as %+v", req)
	}
	// A mismatched address on an isolated socket stays the kernel's answer.
	req = SocketRequest{Op: OpConnect}
	if err := s.decodeAddress(&req, socketInfo{domain: unix.AF_INET, typ: unix.SOCK_STREAM}, vsock, nil); err != nil {
		t.Fatalf("mismatched family on an inet socket: %v", err)
	}
}

func TestRefusalOperationNamesBindAndOutbound(t *testing.T) {
	t.Parallel()
	if got := refusalOperation(OpBind); got != "network-bind" {
		t.Fatalf("bind: %q", got)
	}
	if got := refusalOperation(OpConnect); got != "network-outbound" {
		t.Fatalf("connect: %q", got)
	}
}
