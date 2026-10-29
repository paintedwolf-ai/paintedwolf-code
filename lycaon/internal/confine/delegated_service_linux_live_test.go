//go:build linux

package confine_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/confine/linuxsandbox"
	"github.com/lycaon/lycaon/internal/socketref"
	"github.com/lycaon/lycaon/internal/testutil"
)

// A delegated service acts with its own authority, so a socket grant naming
// it, by its endpoint or by any alias of the same object, is refused and
// routed to explicit host execution. An ordinary grant in the same plan works.
func TestLinuxDelegatedServiceIsRefusedEvenWhenGranted(t *testing.T) {
	self := requireLinuxSandbox(t)
	dir, err := os.MkdirTemp("/tmp", "pw-delegated-") //nolint:usetesting // Short socket path.
	testutil.FailErr(t, "socket directory", err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	daemon := greeter(t, "unix", filepath.Join(dir, "daemon.sock"))
	endpoint := daemon.Addr().String()
	ordinary := greeter(t, "unix", filepath.Join(dir, "ordinary.sock")).Addr().String()
	confine.UseDelegatedEndpointsForTest(t, []linuxsandbox.DelegatedEndpoint{{Service: linuxsandbox.DelegatedNixDaemon, Path: endpoint}})
	symlink, hardlink := filepath.Join(dir, "symlink.sock"), filepath.Join(dir, "hardlink.sock")
	testutil.FailErr(t, "symlink alias", os.Symlink(endpoint, symlink))
	testutil.FailErr(t, "hard-link alias", os.Link(endpoint, hardlink))

	for _, tc := range []struct{ name, requested, refused string }{
		{"exact_endpoint", endpoint, endpoint},
		{"symlink_alias", symlink, endpoint},
		{"hard_link_alias", hardlink, hardlink},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, scope := socketref.WithScope(t.Context())
			defer scope.Close()
			var grants []confine.SocketGrant
			for _, path := range []string{tc.requested, ordinary} {
				resolved, err := confine.ResolveSocketRequest(path)
				testutil.FailErr(t, "resolve "+path, err)
				grant, err := confine.CaptureSocketGrant(ctx, resolved)
				testutil.FailErr(t, "capture "+path, err)
				grants = append(grants, grant)
			}
			c, lease := boundConfinement(t, confine.Confinement{
				Roots: sandboxRoots(t.TempDir()), Network: confine.NetworkDeny, EgressOff: confine.EgressOffOverride,
				SocketGrants: grants,
			})
			out := probeLines(t, self, *c, "unixconnect", tc.requested, ordinary)
			if !strings.Contains(out, "connect "+tc.requested+": EACCES") {
				t.Fatalf("granted delegated service was not refused:\n%s", out)
			}
			if !strings.Contains(out, "connect "+ordinary+": ok host") {
				t.Fatalf("ordinary grant in the same plan did not connect:\n%s", out)
			}
			settled := lease.SettledRefusals(context.Background())
			if refusalTargets(settled)[linuxsandbox.OperationHostServiceDelegation+" "+tc.refused] != confine.RecoverHostExecution {
				t.Fatalf("refusals = %+v, want %s routed to host execution\n%s", settled, tc.refused, out)
			}
		})
	}
}
