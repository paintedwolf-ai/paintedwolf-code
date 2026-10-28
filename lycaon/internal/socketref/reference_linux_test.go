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
)

func TestRetainedListenerSurvivesPathReplacement(t *testing.T) {
	for _, mutation := range []string{"rename", "unlink", "replace", "ancestor"} {
		t.Run(mutation, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "service")
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "socket")
			listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
			if err != nil {
				t.Fatal(err)
			}
			listener.SetUnlinkOnClose(false)
			t.Cleanup(func() { _ = listener.Close() })
			lease, err := Capture(path)
			if err != nil {
				t.Fatal(err)
			}
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
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = decoy.Close() })
			}
			client, err := lease.DialContext(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			accepted, err := listener.AcceptUnix()
			if err != nil {
				t.Fatal(err)
			}
			defer accepted.Close()
			if _, err := client.Write([]byte("approved")); err != nil {
				t.Fatal(err)
			}
			data := make([]byte, 8)
			if _, err := accepted.Read(data); err != nil {
				t.Fatal(err)
			}
			if string(data) != "approved" {
				t.Fatalf("received %q", data)
			}
			if decoy != nil {
				raw, err := decoy.SyscallConn()
				if err != nil {
					t.Fatal(err)
				}
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
		t.Fatal(err)
	}
	if _, err := binding.Acquire(); !errors.Is(err, ErrUnbound) {
		t.Fatalf("reference error = %v", err)
	}
	if _, err := (*Reference)(nil).DialContext(context.Background()); !errors.Is(err, ErrUnbound) {
		t.Fatalf("dial error = %v", err)
	}
}

func TestDuplicateOutlivesClosedOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "socket")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ref, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	file, err := ref.Duplicate()
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := ref.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := ref.Duplicate(); !errors.Is(err, ErrUnbound) {
		t.Fatalf("closed duplicate error = %v", err)
	}
	var dialer net.Dialer
	client, err := dialer.DialContext(t.Context(), "unix", descriptorPath(file))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	accepted, err := listener.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	_ = accepted.Close()
}
