//go:build linux

package linuxsandbox

import (
	"encoding/binary"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRelativeSocketPathsResolveAgainstTheTargetDirectory(t *testing.T) {
	t.Parallel()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("working directory: %v", err)
	}
	root, err := os.MkdirTemp("/tmp", "sn-") //nolint:usetesting // Kernel Unix socket addresses are bounded.
	if err != nil {
		t.Fatalf("socket directory: %v", err)
	}
	defer os.RemoveAll(root)
	path := filepath.Join(root, "socket")
	listener, err := net.Listen("unix", path)
	testutil.FailErr(t, "listen on fixture socket", err)
	defer listener.Close()
	relative, err := filepath.Rel(cwd, path)
	testutil.FailErr(t, "compute relative socket path", err)
	want, err := filepath.EvalSymlinks(path)
	testutil.FailErr(t, "resolve socket symlinks", err)
	resolved, ok := resolveSocketPath(relative, &targetProcess{pid: os.Getpid()})
	if !ok || resolved != want {
		t.Fatalf("relative path: got %q %v, want %q", resolved, ok, want)
	}
	if resolved, ok := resolveSocketPath(path, &targetProcess{pid: -1}); !ok || resolved != want {
		t.Fatalf("absolute path: got %q %v", resolved, ok)
	}
	if resolved, ok := resolveSocketPath(path+".missing", &targetProcess{pid: -1}); ok || resolved != "" {
		t.Fatalf("missing path resolved: %q %v", resolved, ok)
	}

}

func TestUnreadableWorkingDirectoryRefusesRelativeSocketPaths(t *testing.T) {
	t.Parallel()
	exited := exec.Command(os.Args[0], "-test.run=^$")
	if err := exited.Run(); err != nil {
		t.Fatalf("run short-lived process: %v", err)
	}
	gone := &targetProcess{pid: exited.Process.Pid}
	if resolved, ok := resolveSocketPath("relative.sock", gone); ok {
		t.Fatalf("exited target resolved %q", resolved)
	}
	raw := make([]byte, 2, 2+len("relative.sock")+1)
	binary.NativeEndian.PutUint16(raw, unix.AF_UNIX)
	raw = append(append(raw, "relative.sock"...), 0)
	var s supervisor
	for _, op := range []SocketOp{OpConnect, OpBind} {
		req := SocketRequest{Op: op}
		err := s.decodeAddress(&req, socketInfo{domain: unix.AF_UNIX, typ: unix.SOCK_STREAM}, raw, gone)
		if !errors.Is(err, errUnresolvedSocketPath) {
			t.Fatalf("%s: got %v, want errUnresolvedSocketPath", op, err)
		}
		if req.Unix != "" {
			t.Fatalf("%s: unresolved request names %q", op, req.Unix)
		}
	}
}

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
