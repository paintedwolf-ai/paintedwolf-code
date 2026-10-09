package tools_test

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/packageexec"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

type parityDirectIPRuntime struct{}

func (parityDirectIPRuntime) IssuePermit(string, string, string, string, string) {}
func (parityDirectIPRuntime) ConsumePermit(string, string, string, string, string) (bool, error) {
	return true, nil
}
func (parityDirectIPRuntime) Authorized(string, string, string) bool      { return true }
func (parityDirectIPRuntime) LeaseCovers(string, hitl.DirectIPLease) bool { return false }
func (parityDirectIPRuntime) GrantChat(string, hitl.DirectIPLease, string, string, *time.Time) {
}

// Checks that the gate Contained fact matches the confine.Request applied by the executor spawn path.
func TestGateContainedMatchesSpawnRequest(t *testing.T) {
	project := t.TempDir()
	overlayRoot := t.TempDir()
	socketDir, err := os.MkdirTemp("/tmp", "lyc-cp-") //nolint:usetesting // Short socket path.
	testutil.FailErr(t, "MkdirTemp", err)
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	socketPath := filepath.Join(socketDir, "agent.sock")
	ln, err := net.Listen("unix", socketPath)
	testutil.FailErr(t, "listen unix", err)
	t.Cleanup(func() { _ = ln.Close() })
	resolved, err := filepath.EvalSymlinks(socketPath)
	testutil.FailErr(t, "resolve socket", err)
	grant := confine.SocketGrant{ApprovedPath: resolved, ResolvedPath: resolved}

	base := tools.ToolContext{
		Identity: tools.InvocationIdentity{ProjectID: "proj-1",
			SessionID:  "sess-1",
			ToolCallID: "call-1"},
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Path: project, IsPrimary: true}},
			ActiveRootID: "r1"},
	}

	cases := []struct {
		name    string
		mutate  func(*tools.ToolContext)
		overlay []string
	}{
		{name: "command_with_overlay_roots", overlay: []string{overlayRoot}},
		{name: "package_explicit_read", mutate: func(tc *tools.ToolContext) {
			tc.Files.PackageExecution = &packageexec.Execution{SensitiveReads: []string{overlayRoot}, ApprovedReadPaths: []string{filepath.Join(overlayRoot, "cacert.pem")}}
			tc.Files.SessionReadPaths = []string{filepath.Join(overlayRoot, "unrelated-secret")}
		}},
		{name: "socket_grants", mutate: func(tc *tools.ToolContext) {
			tc.Socket.SocketGrants = []confine.SocketGrant{grant}
			// The overlay covers the grant, so spawn finalize consumes no permit.
			tc.Socket.DurableSocketGrants = []confine.SocketGrant{grant}
		}},
		{name: "socks_proxy_env", mutate: func(tc *tools.ToolContext) { tc.Local.SocksProxyEnv = true }},
		{name: "direct_ip_declared", mutate: func(tc *tools.ToolContext) {
			tc.Direct.DirectIPRequested = true
			tc.Direct.DirectIPDeclared = []string{"udp:123"}
			tc.Direct.DirectIPCapabilityRuntime = parityDirectIPRuntime{}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tctx := base
			if tc.mutate != nil {
				tc.mutate(&tctx)
			}
			gateReq := hitl.ActionConfineRequest(tools.ActionConfineInputsForContext(tctx, tc.overlay))
			spawnReq, reject := tools.ConfineRequestForSpawn(context.Background(), tctx, tc.overlay)
			if reject != nil {
				t.Fatalf("spawn request rejected: %+v", reject)
			}
			if !reflect.DeepEqual(gateReq, spawnReq) {
				t.Fatalf("gate request diverged from spawn request:\ngate:  %+v\nspawn: %+v", gateReq, spawnReq)
			}
			gateContained := hitl.ContainedForRequest(gateReq)
			spawnContained := hitl.ContainedFromConfinement(confine.DefaultConfinement(spawnReq))
			if !reflect.DeepEqual(gateContained, spawnContained) {
				t.Fatalf("gate Contained diverged from executor Contained:\ngate:  %+v\nspawn: %+v", gateContained, spawnContained)
			}
			if tc.overlay != nil && gateContained.Active() {
				found := false
				for _, root := range gateContained.Roots {
					if root == overlayRoot {
						found = true
					}
				}
				if !found {
					t.Fatalf("overlay root missing from gate Contained.Roots: %+v", gateContained.Roots)
				}
			}
			if tctx.Direct.DirectIPRequested && gateContained.Active() {
				foundNarrowed := false
				for _, permit := range gateContained.EffectiveBoundaryPermits() {
					if permit.Kind == hitl.BoundaryPermitDirectIP && permit.Digest != "enabled" {
						foundNarrowed = true
					}
				}
				if !foundNarrowed {
					t.Fatalf("declared direct-IP narrowing missing from permits: %+v", gateContained.EffectiveBoundaryPermits())
				}
			}
		})
	}
}
