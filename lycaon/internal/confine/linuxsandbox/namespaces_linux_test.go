//go:build linux

package linuxsandbox

import (
	"errors"
	"os"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestNamespaceLaunchCreatesPrivateIPC(t *testing.T) {
	t.Parallel()
	const required = unix.CLONE_NEWUSER | unix.CLONE_NEWNS | unix.CLONE_NEWPID | unix.CLONE_NEWNET | unix.CLONE_NEWIPC
	if flags := namespaceAttr().Cloneflags; flags != required {
		t.Fatalf("namespace clone flags = %#x, want %#x", flags, required)
	}
}

func TestNamespaceAttestationRequiresPrivateIPC(t *testing.T) {
	t.Parallel()
	host := make(map[string]string)
	for _, kind := range NamespaceKinds() {
		host[kind] = "different namespace"
	}
	testutil.FailErr(t, "accept private namespaces", namespacesDiffer(host))
	ipc, err := os.Readlink("/proc/self/ns/ipc")
	testutil.FailErr(t, "current IPC namespace", err)
	for _, missing := range []bool{false, true} {
		host[NamespaceIPC] = ipc
		if missing {
			delete(host, NamespaceIPC)
		}
		var unavailable *Unavailability
		if err := namespacesDiffer(host); !errors.As(err, &unavailable) || unavailable.Code != UnavailableNamespaces {
			t.Fatalf("missing IPC identity %v: attestation accepted shared or unattested IPC: %v", missing, err)
		}
	}
}
