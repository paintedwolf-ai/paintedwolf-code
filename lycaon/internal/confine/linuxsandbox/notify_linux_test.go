//go:build linux

package linuxsandbox

import (
	"encoding/binary"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestRelativeSocketPathsResolveAgainstTheTargetDirectory(t *testing.T) {
	t.Parallel()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("working directory: %v", err)
	}
	resolved, ok := resolveSocketPath("nested/relative.sock", &targetProcess{pid: os.Getpid()})
	want := filepath.Join(cwd, "nested/relative.sock")
	if real, err := filepath.EvalSymlinks(cwd); err == nil {
		want = filepath.Join(real, "nested/relative.sock")
	}
	if !ok || resolved != want {
		t.Fatalf("relative path: got %q %v, want %q", resolved, ok, want)
	}
	if resolved, ok := resolveSocketPath("/run/absolute.sock", &targetProcess{pid: -1}); !ok || resolved != "/run/absolute.sock" {
		t.Fatalf("absolute path: got %q %v", resolved, ok)
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
