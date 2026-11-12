//go:build darwin || linux

package confine

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/socketref"
	"github.com/lycaon/lycaon/internal/testutil"
)

func listenUnix(t *testing.T, path string) {
	t.Helper()
	listener, err := net.Listen("unix", path)
	testutil.FailErr(t, "listen", err)
	t.Cleanup(func() { _ = listener.Close() })
}

// A host service reaches an action through the platform's service
// realization: an exact path rule only inside a protected service directory,
// or a retained object the connect mediator dials.
func TestServeHostSocketAdmitsTheHostServiceItself(t *testing.T) {
	dir, err := socketref.ServiceDirectory()
	testutil.FailErr(t, "service directory", err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "service")
	listenUnix(t, path)

	service, release, err := ServeHostSocket(path)
	testutil.FailErr(t, "serve host socket", err)
	defer release()
	prepared, err := prepareRequest(Request{HostServices: []HostService{service}})
	testutil.FailErr(t, "prepare request", err)

	switch socketref.Platform().Service {
	case socketref.KernelPathname:
		canonical := fspath.CanonicalPath(path)
		if len(prepared.HostServicePaths) != 1 || prepared.HostServicePaths[0] != canonical || len(prepared.SocketGrants) != 0 {
			t.Fatalf("service paths %v, grants %v", prepared.HostServicePaths, prepared.SocketGrants)
		}
		var profile strings.Builder
		testutil.FailErr(t, "network rules", writeNetworkRules(&profile, Confinement{Network: NetworkDeny, HostServicePaths: prepared.HostServicePaths}))
		if !strings.Contains(profile.String(), "(allow network-outbound (literal "+sbplString(canonical)+"))") {
			t.Fatalf("profile does not admit the service:\n%s", profile.String())
		}

		outside, err := os.MkdirTemp("/tmp", "pw-outside-") //nolint:usetesting // A socket path must stay short.
		testutil.FailErr(t, "outside dir", err)
		t.Cleanup(func() { _ = os.RemoveAll(outside) })
		foreign := filepath.Join(outside, "service")
		listenUnix(t, foreign)
		if _, _, err := ServeHostSocket(foreign); err == nil {
			t.Fatal("a socket outside a service directory was admitted by its path")
		}
	case socketref.RetainedObject:
		if len(prepared.SocketGrants) != 1 || prepared.SocketGrants[0].Binding.Mode() != socketref.RetainedObject || len(prepared.HostServicePaths) != 0 {
			t.Fatalf("grants %v, service paths %v", prepared.SocketGrants, prepared.HostServicePaths)
		}
	default:
		t.Fatalf("no host service realization on %s", socketref.Platform().Service)
	}
}
