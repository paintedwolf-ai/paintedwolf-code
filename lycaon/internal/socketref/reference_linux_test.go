//go:build linux

package socketref

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRetainedListenerSurvivesPathReplacement(t *testing.T) {
	for _, mutation := range []string{"rename", "unlink", "replace", "ancestor"} {
		t.Run(mutation, func(t *testing.T) {
			root := shortSocketRoot(t)
			dir := filepath.Join(root, "service")
			if err := os.Mkdir(dir, 0700); err != nil {
				testutil.FailErr(t, "create fixture directory", err)
			}
			path := filepath.Join(dir, "socket")
			listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
			testutil.FailErr(t, "listen on fixture Unix socket", err)
			listener.SetUnlinkOnClose(false)
			t.Cleanup(func() { _ = listener.Close() })
			lease, err := Capture(path)
			testutil.FailErr(t, "capture socket reference", err)
			t.Cleanup(lease.Close)
			switch mutation {
			case "rename":
				err = os.Rename(path, path+".old")
			case "unlink", "replace":
				err = os.Remove(path)
			case "ancestor":
				err = os.Rename(dir, dir+".old")
				if err == nil {
					err = os.Mkdir(dir, 0700)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			var decoy *net.UnixListener
			if mutation == "replace" || mutation == "ancestor" {
				decoy, err = net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
				testutil.FailErr(t, "listen on fixture Unix socket", err)
				t.Cleanup(func() { _ = decoy.Close() })
			}
			client, err := lease.DialContext(t.Context())
			testutil.FailErr(t, "dial retained socket", err)
			defer client.Close()
			accepted, err := listener.AcceptUnix()
			testutil.FailErr(t, "accept fixture Unix connection", err)
			defer accepted.Close()
			if _, err := client.Write([]byte("approved")); err != nil {
				testutil.FailErr(t, "write through retained socket", err)
			}
			data := make([]byte, 8)
			if _, err := accepted.Read(data); err != nil {
				testutil.FailErr(t, "read approved socket payload", err)
			}
			if string(data) != "approved" {
				t.Fatalf("received %q", data)
			}
			if decoy != nil {
				raw, err := decoy.SyscallConn()
				testutil.FailErr(t, "inspect decoy listener", err)
				var ready int
				var pollErr error
				if err := raw.Control(func(fd uintptr) {
					ready, pollErr = unix.Poll([]unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}, 0)
				}); err != nil {
					t.Fatal(err)
				}
				if pollErr != nil || ready != 0 {
					t.Fatalf("decoy accept readiness = %d, error = %v", ready, pollErr)
				}
			}
		})
	}
}

func TestUnboundBindingDoesNotReopenExistingPath(t *testing.T) {
	var binding Binding
	if err := json.Unmarshal([]byte(`{"id":"prior-engine-reference"}`), &binding); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	if _, err := binding.Acquire(); !errors.Is(err, ErrUnbound) {
		t.Fatalf("reference error = %v", err)
	}
	if _, err := (*Reference)(nil).DialContext(context.Background()); !errors.Is(err, ErrUnbound) {
		t.Fatalf("dial error = %v", err)
	}
}

func TestDuplicateOutlivesClosedOwner(t *testing.T) {
	path := filepath.Join(shortSocketRoot(t), "socket")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	testutil.FailErr(t, "listen on fixture Unix socket", err)
	defer listener.Close()
	ref, err := Open(path)
	testutil.FailErr(t, "open retained socket", err)
	file, err := ref.Duplicate()
	testutil.FailErr(t, "duplicate retained reference", err)
	defer file.Close()
	if err := ref.Close(); err != nil {
		testutil.FailErr(t, "close retained reference", err)
	}
	if _, err := ref.Duplicate(); !errors.Is(err, ErrUnbound) {
		t.Fatalf("closed duplicate error = %v", err)
	}
	var dialer net.Dialer
	client, err := dialer.DialContext(t.Context(), "unix", descriptorPath(file))
	testutil.FailErr(t, "dial retained descriptor path", err)
	defer client.Close()
	accepted, err := listener.AcceptUnix()
	testutil.FailErr(t, "accept fixture Unix connection", err)
	_ = accepted.Close()
}

func shortSocketRoot(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "sr-") //nolint:usetesting // Kernel Unix socket addresses are bounded.
	if err != nil {
		t.Fatalf("create socket root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	return root
}
