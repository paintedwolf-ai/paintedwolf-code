//go:build linux

package socketref

import (
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestLeaseTransferAndReleaseAreDeterministic(t *testing.T) {
	path := filepath.Join(shortSocketRoot(t), "socket")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	for range 128 {
		original, err := Capture(path)
		if err != nil {
			t.Fatal(err)
		}
		binding := original.Binding()
		encoded, err := json.Marshal(binding)
		if err != nil {
			t.Fatal(err)
		}
		var restored Binding
		if err := json.Unmarshal(encoded, &restored); err != nil {
			t.Fatal(err)
		}
		accepted, err := restored.Acquire()
		if err != nil {
			t.Fatal(err)
		}
		file, err := accepted.Duplicate()
		if err != nil {
			t.Fatal(err)
		}
		original.Close()
		original.Close()
		if _, err := original.Duplicate(); !errors.Is(err, ErrUnbound) {
			t.Fatalf("closed lease duplicate: %v", err)
		}
		bindings.Lock()
		entry := bindings.refs[binding.ID]
		fd := int(entry.reference.file.Fd())
		owners := entry.owners
		bindings.Unlock()
		if owners != 1 {
			t.Fatalf("live owners = %d", owners)
		}
		accepted.Close()
		var stat unix.Stat_t
		if err := unix.Fstat(fd, &stat); !errors.Is(err, unix.EBADF) {
			t.Fatalf("released reference descriptor: %v", err)
		}
		if _, err := restored.Acquire(); !errors.Is(err, ErrUnbound) {
			t.Fatalf("released binding acquired: %v", err)
		}
		if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
			t.Fatalf("execution duplicate closed with grant: %v", err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		bindings.Lock()
		remaining := len(bindings.refs)
		bindings.Unlock()
		if remaining != 0 {
			t.Fatalf("retained destinations after release = %d", remaining)
		}
	}
}

func TestLastAuthorityReleaseCannotBeResurrectedDuringConnect(t *testing.T) {
	path := filepath.Join(shortSocketRoot(t), "socket")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	lease, err := Capture(path)
	if err != nil {
		t.Fatal(err)
	}
	binding := lease.Binding()
	ref, finish, err := lease.operation()
	if err != nil {
		t.Fatal(err)
	}
	lease.Close()
	if _, err := binding.Acquire(); !errors.Is(err, ErrUnbound) {
		t.Fatalf("retired binding reacquired: %v", err)
	}
	client, err := ref.DialContext(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	accepted, err := listener.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	_ = accepted.Close()
	finish()
	if _, err := ref.Duplicate(); !errors.Is(err, ErrUnbound) {
		t.Fatalf("reference retained after operation: %v", err)
	}
}
