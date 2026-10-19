//go:build linux

package linuxsandbox

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/testutil"
)

func fixtureUnixListener(t *testing.T, path string) *net.UnixListener {
	t.Helper()
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	testutil.FailErr(t, "create fixture-owned endpoint", err)
	t.Cleanup(func() { _ = listener.Close() })
	return listener
}

func endpointDirectory(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "pw-endpoint-") //nolint:usetesting // Unix socket path limit.
	testutil.FailErr(t, "create short socket directory", err)
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	return root
}

func TestDelegatedServiceSocketIdentityIncludesAliases(t *testing.T) {
	root := endpointDirectory(t)
	service, alias, hardlink, ordinary := filepath.Join(root, "daemon"), filepath.Join(root, "alias"), filepath.Join(root, "hardlink"), filepath.Join(root, "agent")
	fixtureUnixListener(t, service)
	fixtureUnixListener(t, ordinary)
	testutil.FailErr(t, "create alias to delegated service", os.Symlink(service, alias))
	testutil.FailErr(t, "create same-inode delegated alias", os.Link(service, hardlink))
	policy := []delegatedServiceEndpoint{{DelegatedNixDaemon, service}}
	for _, path := range []string{service, alias, hardlink, ordinary} {
		endpoint := pinUnixEndpoint(path, policy)
		defer endpoint.close()
		testutil.FailErr(t, "pin observed socket", endpoint.openErr)
		decision := (Network{Mode: NetworkDeny, SocketPaths: []string{path}}).Decide(SocketRequest{Op: OpConnect, Unix: path, Stream: true, Delegated: endpoint.delegated})
		if path == ordinary {
			if endpoint.delegated != "" || decision.Action != Perform {
				t.Fatal("ordinary agent socket inherited delegated service authority")
			}
		} else if endpoint.delegated != DelegatedNixDaemon || decision.Action != Refuse || decision.Refusal == nil || decision.Refusal.Operation != "host-service-delegation" {
			t.Fatalf("ordinary socket grant admitted delegated endpoint: %+v %+v", endpoint, decision)
		}
	}
}

func TestPinnedUnixEndpointCannotFollowRecipientReplacement(t *testing.T) {
	root := endpointDirectory(t)
	ordinary, service, alias := filepath.Join(root, "agent"), filepath.Join(root, "daemon"), filepath.Join(root, "alias")
	allowed, delegated := fixtureUnixListener(t, ordinary), fixtureUnixListener(t, service)
	testutil.FailErr(t, "create approved recipient alias", os.Symlink(ordinary, alias))
	endpoint := pinUnixEndpoint(alias, []delegatedServiceEndpoint{{DelegatedNixDaemon, service}})
	defer endpoint.close()
	testutil.FailErr(t, "pin approved ordinary endpoint", endpoint.openErr)
	if endpoint.delegated != "" {
		t.Fatal("ordinary approved endpoint was delegated")
	}
	testutil.FailErr(t, "replace fixture-owned alias", os.Remove(alias))
	testutil.FailErr(t, "substitute delegated endpoint after check", os.Symlink(service, alias))
	client, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	testutil.FailErr(t, "create client socket", err)
	defer func() { _ = unix.Close(client) }()
	testutil.FailErr(t, "connect held approved endpoint", endpoint.connect(client))
	testutil.FailErr(t, "bound ordinary accept", allowed.SetDeadline(time.Now().Add(time.Second)))
	connection, err := allowed.AcceptUnix()
	testutil.FailErr(t, "ordinary approved recipient accepted connection", err)
	_ = connection.Close()
	testutil.FailErr(t, "bound delegated accept", delegated.SetDeadline(time.Now().Add(20*time.Millisecond)))
	connection, err = delegated.AcceptUnix()
	if err == nil {
		_ = connection.Close()
		t.Fatal("recipient replacement redirected the approved connection")
	}
}

func TestDelegatedEndpointAbsentOrInvalidDoesNotFallThrough(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "daemon")
	endpoint := pinUnixEndpoint(missing, []delegatedServiceEndpoint{{DelegatedNixDaemon, missing}})
	defer endpoint.close()
	if endpoint.delegated != DelegatedNixDaemon || !errors.Is(endpoint.openErr, unix.ENOENT) {
		t.Fatalf("missing standard endpoint lost its service classification: %+v", endpoint)
	}
	ordinary := filepath.Join(t.TempDir(), "file")
	testutil.FailErr(t, "create non-socket fixture", os.WriteFile(ordinary, []byte("not a socket"), 0o600))
	invalid := pinUnixEndpoint(ordinary, nil)
	defer invalid.close()
	if !errors.Is(invalid.openErr, unix.ENOTSOCK) || !errors.Is(invalid.connect(-1), unix.ENOTSOCK) {
		t.Fatal("invalid recipient fell through to an unpinned pathname connect")
	}
}
