//go:build linux

package socketref

import (
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestLeaseTransferAndReleaseAreDeterministic(t *testing.T) {
	path := filepath.Join(shortSocketRoot(t), "socket")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	testutil.FailErr(t, "listen on fixture Unix socket", err)
	defer listener.Close()
	for range 128 {
		original, err := Capture(path)
		testutil.FailErr(t, "capture socket reference", err)
		binding := original.Binding()
		encoded, err := json.Marshal(binding)
		testutil.FailErr(t, "encode retained binding", err)
		var restored Binding
		if err := json.Unmarshal(encoded, &restored); err != nil {
			testutil.FailErr(t, "unmarshal JSON document", err)
		}
		accepted, err := restored.Acquire()
		testutil.FailErr(t, "acquire restored binding", err)
		file, err := accepted.Duplicate()
		testutil.FailErr(t, "duplicate accepted lease", err)
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
			testutil.FailErr(t, "close execution duplicate", err)
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
	testutil.FailErr(t, "listen on fixture Unix socket", err)
	defer listener.Close()
	lease, err := Capture(path)
	testutil.FailErr(t, "capture socket reference", err)
	binding := lease.Binding()
	ref, finish, err := lease.operation()
	testutil.FailErr(t, "admit lease operation", err)
	lease.Close()
	if _, err := binding.Acquire(); !errors.Is(err, ErrUnbound) {
		t.Fatalf("retired binding reacquired: %v", err)
	}
	client, err := ref.DialContext(t.Context())
	testutil.FailErr(t, "dial retained socket", err)
	defer client.Close()
	accepted, err := listener.AcceptUnix()
	testutil.FailErr(t, "accept fixture Unix connection", err)
	_ = accepted.Close()
	finish()
	if _, err := ref.Duplicate(); !errors.Is(err, ErrUnbound) {
		t.Fatalf("reference retained after operation: %v", err)
	}
}
