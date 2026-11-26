package tools_test

import (
	"net"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/confine/admissiontest"
	"github.com/lycaon/lycaon/internal/grantedpath"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/packageexec"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/socketref"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

type parityDirectIPRuntime struct{}

func (parityDirectIPRuntime) IssuePermit(hitl.EffectPermitIdentity, string, string, string) {}
func (parityDirectIPRuntime) ConsumePermit(hitl.EffectPermitIdentity, string, string, string) (bool, error) {
	return true, nil
}
func (parityDirectIPRuntime) Authorized(hitl.EffectPermitIdentity, string) bool { return true }
func (parityDirectIPRuntime) LeaseCovers(string, hitl.DirectIPLease) bool       { return false }
func (parityDirectIPRuntime) GrantChat(string, hitl.DirectIPLease, string, string, *time.Time) bool {
	return true
}

func TestGateContainedMatchesSpawnRequest(t *testing.T) {
	admissiontest.Enable(t)
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
	ctx, scope := socketref.WithScope(t.Context())
	defer scope.Close()
	grant, err := confine.CaptureSocketGrant(ctx, confine.SocketGrant{ApprovedPath: resolved, ResolvedPath: resolved})
	testutil.FailErr(t, "capture reviewed socket", err)

	base := tools.ToolContext{
		Identity: tools.InvocationIdentity{ProjectID: "proj-1",
			SessionID:  "sess-1",
			ToolCallID: "call-1"},
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Path: project, IsPrimary: true}},
			ActiveRootID: "r1"},
	}

	cases := []struct {
		name       string
		mutate     func(*tools.ToolContext)
		overlay    []string
		sandboxOff bool
	}{
		{name: "command_with_overlay_roots", overlay: []string{overlayRoot}},
		{name: "overlay_with_sandbox_off", overlay: []string{overlayRoot}, sandboxOff: true},
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
			if tc.sandboxOff {
				t.Setenv("LYCAON_SANDBOX", "off")
			}
			tctx := base
			if tc.mutate != nil {
				tc.mutate(&tctx)
			}
			gateReq := hitl.ActionConfineRequest(tools.ActionConfineInputsForContext(tctx, tc.overlay))
			spawnReq, reject := tools.ConfineRequestForSpawn(ctx, tctx, tc.overlay)
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
				if !confine.PathWithinRoots(overlayRoot, gateContained.WriteRoots) || confine.PathWithinRoots(overlayRoot, gateContained.Roots) {
					t.Fatalf("reviewed root %q, writable roots %v, attached roots %v", overlayRoot, gateContained.WriteRoots, gateContained.Roots)
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

func TestGrantedPathSourceProjectsDirectionalAuthorityToConfinement(t *testing.T) {
	admissiontest.Enable(t)
	project := t.TempDir()
	tempDir := t.TempDir()
	readFile := filepath.Join(tempDir, "in.csv")
	writeOnlyDir := filepath.Join(tempDir, "out")
	rwDir := filepath.Join(tempDir, "exchange")
	testutil.FailErr(t, "seed read file", os.WriteFile(readFile, []byte("a"), 0o600))
	testutil.FailErr(t, "mkdir write-only", os.MkdirAll(writeOnlyDir, 0o700))
	testutil.FailErr(t, "mkdir rw", os.MkdirAll(rwDir, 0o700))

	rt := grantedpath.NewRuntime()
	_, err := rt.Grant("sess-1", grantedpath.Grant{ID: "g-read", Path: readFile, Mode: grantedpath.ModeRead})
	testutil.FailErr(t, "grant read file", err)
	_, err = rt.Grant("sess-1", grantedpath.Grant{ID: "g-wo", Path: writeOnlyDir, Mode: grantedpath.ModeWriteOnly, Tree: true})
	testutil.FailErr(t, "grant write-only dir", err)
	_, err = rt.Grant("sess-1", grantedpath.Grant{ID: "g-rw", Path: rwDir, Mode: grantedpath.ModeWrite, Tree: true})
	testutil.FailErr(t, "grant rw dir", err)
	t.Cleanup(tools.SetGrantedPathSource(rt.ListEffective))

	tctx := tools.ToolContext{
		Identity: tools.InvocationIdentity{ProjectID: "proj-1", SessionID: "sess-1"},
		Source:   tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Path: project, IsPrimary: true}}, ActiveRootID: "r1"},
	}
	req := hitl.ActionConfineRequest(tools.ActionConfineInputsForContext(tctx, nil))
	c, ok := confine.DefaultConfinement(req)
	if !ok || c == nil {
		t.Fatal("default confinement unavailable")
	}
	fs := confine.BoundaryOf(c).Filesystem
	if !fs.Verdict(confine.AccessRead, readFile).Allowed || fs.Verdict(confine.AccessWrite, readFile).Allowed {
		t.Fatalf("exact read file projection broke direction: read=%+v write=%+v", fs.Verdict(confine.AccessRead, readFile), fs.Verdict(confine.AccessWrite, readFile))
	}
	if !fs.Verdict(confine.AccessWrite, filepath.Join(writeOnlyDir, "res.csv")).Allowed || fs.Verdict(confine.AccessRead, filepath.Join(writeOnlyDir, "res.csv")).Allowed {
		t.Fatalf("write-only dir projection broke direction")
	}
	if !fs.Verdict(confine.AccessWrite, filepath.Join(rwDir, "swap.csv")).Allowed || !fs.Verdict(confine.AccessRead, filepath.Join(rwDir, "swap.csv")).Allowed {
		t.Fatalf("read-write dir projection broke direction")
	}
}
