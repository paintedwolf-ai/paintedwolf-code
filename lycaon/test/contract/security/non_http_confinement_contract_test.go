package contract

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/egressproxy"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/socketref"
	"github.com/lycaon/lycaon/internal/tools"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Confinement integrity for fail-closed mediation, emitters, Contained, and grants.

// A front door that cannot bind leaves no half-started broker: the HTTP
// listener is released, and an action needing mediation refuses before launch.
func TestNonHTTPContractProxyStartupFailureDeniesAndRollsBack(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	contractcheck.FailErr(t, "hold the SOCKS port", err)
	t.Cleanup(func() { _ = occupied.Close() })
	var httpAddr string
	broker := egressproxy.New(func(context.Context, string, egressproxy.Endpoint) bool { return false })
	_, err = broker.Start(egressproxy.Options{
		ListenHTTP: func(network, address string) (net.Listener, error) {
			ln, err := net.Listen(network, address)
			if err == nil {
				httpAddr = ln.Addr().String()
			}
			return ln, err
		},
		ListenSOCKS: func(network, _ string) (net.Listener, error) {
			return net.Listen(network, occupied.Addr().String())
		},
	})
	if !errors.Is(err, syscall.EADDRINUSE) {
		t.Fatalf("Start with an occupied SOCKS port = %v, want EADDRINUSE", err)
	}
	if httpAddr == "" {
		t.Fatal("Start never bound the HTTP front door")
	}
	if broker.Addr() != "" || broker.SocksAddr() != "" {
		t.Fatalf("failed Start kept listeners: http=%q socks=%q", broker.Addr(), broker.SocksAddr())
	}
	rebound, err := net.Listen("tcp", httpAddr)
	if err != nil {
		t.Fatalf("HTTP listener port %s still held after the failed Start: %v", httpAddr, err)
	}
	_ = rebound.Close()

	confine.TestingSetAutoConfine(t)
	t.Setenv("LYCAON_BYPASS_APPROVALS", "")
	t.Setenv("LYCAON_SANDBOX", "")
	t.Setenv("LYCAON_SANDBOX_NETWORK", "")
	requireNonHTTPConfinement(t)
	t.Cleanup(confine.SetBrokerUnavailableForTest(os.ErrInvalid))
	c, ok := confine.DefaultConfinement(confine.Request{Roots: []string{t.TempDir()}})
	if !ok || c == nil || c.Network != confine.NetworkProxyOnly {
		t.Fatalf("default must still compile the proxy-only intent, got ok=%v c=%+v", ok, c)
	}
	if _, err := confine.BindAction(c, confine.EgressCommand{ToolCallID: "contract"}); err == nil {
		t.Fatal("broker failure must refuse the action before launch")
	}
}

// Socket grants render as exact literal predicates only, and direct IP opens
// remote IP outbound without a blanket network allow.
func TestNonHTTPContractSocketEmitterLiteralOnlyAndDirectRemoteIP(t *testing.T) {
	t.Parallel()
	profile, err := confine.BuildSeatbeltProfile(confine.Confinement{Roots: []string{"/proj"}, Network: confine.NetworkDirectIP})
	contractcheck.FailErr(t, "BuildProfile direct", err)
	if !strings.Contains(profile, `(allow network-outbound (remote ip "*:*"))`) {
		t.Fatalf("direct profile missing remote IP allow:\n%s", profile)
	}
	if strings.Contains(profile, "(allow network*)") {
		t.Fatal("direct profile must not emit network*")
	}

	dir := shortUnixTempDir(t)
	sockPath := filepath.Join(dir, "svc.sock")
	ln, err := net.Listen("unix", sockPath)
	contractcheck.FailErr(t, "listen unix", err)
	t.Cleanup(func() { _ = ln.Close() })
	ctx, scope := socketref.WithScope(t.Context())
	t.Cleanup(scope.Close)
	grant, err := confine.ResolveSocketRequest(sockPath)
	contractcheck.FailErr(t, "ResolveSocketRequest", err)
	grant, err = confine.CaptureSocketGrant(ctx, grant)
	contractcheck.FailErr(t, "capture reviewed socket", err)
	c := confine.Confinement{Roots: []string{dir}, Network: confine.NetworkDeny, SocketGrants: []confine.SocketGrant{grant}}
	want := grant.ResolvedPath
	if runtime.GOOS == "darwin" {
		lease, err := confine.BindAction(&c, confine.EgressCommand{ToolCallID: t.Name()})
		contractcheck.FailErr(t, "bind socket endpoints", err)
		t.Cleanup(func() { _ = lease.Close(context.Background()) })
		args, _, err := confine.ResolveSocketArguments(&c, []string{"{{paintedwolf-socket:" + grant.ApprovedPath + "}}"}, nil)
		contractcheck.FailErr(t, "resolve socket endpoint", err)
		want, err = filepath.EvalSymlinks(args[0])
		contractcheck.FailErr(t, "resolve endpoint literal", err)
	} else {
		c.SocketGrants[0].Binding = socketref.Binding{Enforcement: socketref.KernelPathname}
	}
	profile, err = confine.BuildSeatbeltProfile(c)
	contractcheck.FailErr(t, "BuildProfile with socket grant", err)
	if !strings.Contains(profile, `(allow network-outbound (literal "`+want+`"))`) {
		t.Fatalf("socket grant missing its exact literal:\n%s", profile)
	}
	for _, line := range strings.Split(profile, "\n") {
		if !strings.HasPrefix(line, "(allow network") {
			continue
		}
		if !strings.HasPrefix(line, "(allow network-outbound (literal ") {
			t.Errorf("socket-only profile allows network beyond exact literals: %s", line)
		}
	}
}

// Direct IP is not a held boundary: a command under it classifies and gates
// like an unconfined one, while proxy and deny egress hold.
func TestNonHTTPContractEgressContainedExcludesDirectAndBypass(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	command := func(c hitl.Contained) hitl.ProposedAction {
		return hitl.ProposedAction{
			Invocation: hitl.ActionInvocation{Tool: "command", Args: map[string]any{"command": "go test ./..."}},
			Scope:      hitl.ActionScope{ProjectDir: dir},
			Execution:  hitl.ActionExecution{Contained: c},
		}
	}
	jailed := func(egress string) hitl.Contained {
		return hitl.Contained{FSJailed: true, Egress: egress, Roots: []string{dir}, DirectIP: egress == hitl.ContainedEgressDirectIP}
	}
	for _, tc := range []struct {
		name      string
		contained hitl.Contained
		want      settings.ReversibilityTier
	}{
		{"proxy", jailed(hitl.ContainedEgressProxy), settings.TierRecoverable},
		{"deny", jailed(hitl.ContainedEgressDeny), settings.TierRecoverable},
		{"direct_ip", jailed(hitl.ContainedEgressDirectIP), settings.TierIrreversible},
		{"bypass", hitl.Contained{}, settings.TierIrreversible},
	} {
		action := command(tc.contained)
		if got := settings.ClassifyTier(action); got != tc.want {
			t.Errorf("%s: ClassifyTier = %s, want %s", tc.name, settings.TierLabel(got), settings.TierLabel(tc.want))
		}
		res := evalBundled(t, action)
		if held := tc.want == settings.TierRecoverable; res.AutoApproved() != held {
			t.Errorf("%s: auto-approved = %v, want %v: %+v", tc.name, res.AutoApproved(), held, res)
		}
	}
}

// Contained projects socket count, digest and direct IP from the confinement.
func TestNonHTTPContractContainedDerivesFromSameRequestAsProfile(t *testing.T) {
	t.Parallel()
	dir := shortUnixTempDir(t)
	sockPath := filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", sockPath)
	contractcheck.FailErr(t, "listen unix", err)
	t.Cleanup(func() { _ = ln.Close() })
	grant, err := confine.ResolveSocketRequest(sockPath)
	contractcheck.FailErr(t, "ResolveSocketRequest", err)

	synthetic := &confine.Confinement{
		Roots:        []string{dir},
		Network:      confine.NetworkDirectIP,
		SocketGrants: []confine.SocketGrant{grant},
	}
	fromConf := hitl.ContainedFromConfinement(synthetic, true)
	if fromConf.SocketCount != 1 || fromConf.SocketPathsDigest != confine.SocketPathsDigest([]confine.SocketGrant{grant}) || !fromConf.DirectIP {
		t.Fatalf("Contained must project socket digest/count and DirectIP: %+v", fromConf)
	}
	if fromConf.Egress != hitl.ContainedEgressDirectIP {
		t.Fatalf("Contained.Egress=%q want direct_ip", fromConf.Egress)
	}
}

func shortUnixTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "lyc-nh-") //nolint:usetesting // Short socket path.
	contractcheck.FailErr(t, "mkdir temp", err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestNonHTTPContractWitnessAndGrantKeyVaryWithBoundary(t *testing.T) {
	t.Parallel()
	base := hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{
			Tool: "command",
			Args: map[string]any{"command": "true"},
		},
		Scope: hitl.ActionScope{
			ProjectDir: "/proj",
		},
		Execution: hitl.ActionExecution{
			Contained: hitl.Contained{
				FSJailed: true,
				Egress:   hitl.ContainedEgressProxy,
				Roots:    []string{"/proj"},
			},
		},
	}
	k0 := hitl.GrantKey(base)

	withRoot := base
	withRoot.Execution.Contained.Roots = []string{"/proj", "/other"}
	if hitl.GrantKey(withRoot) == k0 {
		t.Fatal("roots must change GrantKey")
	}

	withSock := base
	withSock.Execution.Contained.SocketPathsDigest = "sock-a"
	withSock.Execution.Contained.SocketCount = 1
	if hitl.GrantKey(withSock) != k0 {
		t.Fatal("socket overlay changed GrantKey")
	}

	direct := base
	direct.Execution.Contained.Egress = hitl.ContainedEgressDirectIP
	direct.Execution.Contained.DirectIP = true
	if hitl.GrantKey(direct) == k0 {
		t.Fatal("direct IP must change GrantKey")
	}

	mediated := base
	mediated.Execution.Contained.Egress = hitl.ContainedEgressDeny
	if hitl.GrantKey(mediated) == k0 {
		t.Fatal("mediated egress label must change GrantKey")
	}

}

// A socket grant whose symlink was repointed after review is refused at spawn
// finalize, while the same grant finalizes before the repoint.
func TestNonHTTPContractStaleSymlinkGrantRevalidateDenies(t *testing.T) {
	t.Parallel()
	dir := shortUnixTempDir(t)
	realA := filepath.Join(dir, "a.sock")
	realB := filepath.Join(dir, "b.sock")
	link := filepath.Join(dir, "link.sock")
	lnA, err := net.Listen("unix", realA)
	contractcheck.FailErr(t, "listen a", err)
	t.Cleanup(func() { _ = lnA.Close() })
	lnB, err := net.Listen("unix", realB)
	contractcheck.FailErr(t, "listen b", err)
	t.Cleanup(func() { _ = lnB.Close() })
	contractcheck.FailErr(t, "symlink", os.Symlink(realA, link))

	ctx, scope := socketref.WithScope(t.Context())
	t.Cleanup(scope.Close)
	grant, err := confine.ResolveSocketRequest(link)
	contractcheck.FailErr(t, "ResolveSocketRequest", err)
	grant, err = confine.CaptureSocketGrant(ctx, grant)
	contractcheck.FailErr(t, "capture reviewed socket", err)
	// The durable overlay covers the grant, so finalize needs no permit.
	tctx := tools.ToolContext{Socket: tools.InvocationSocket{
		SocketGrants:        []confine.SocketGrant{grant},
		DurableSocketGrants: []confine.SocketGrant{grant},
	}}
	if got, reject := tools.FinalizeSocketGrantsForSpawn(ctx, tctx); reject != nil || len(got) != 1 {
		t.Fatalf("fresh grant must finalize: grants=%+v reject=%+v", got, reject)
	}

	contractcheck.FailErr(t, "remove link", os.Remove(link))
	contractcheck.FailErr(t, "repoint symlink", os.Symlink(realB, link))
	got, reject := tools.FinalizeSocketGrantsForSpawn(ctx, tctx)
	if reject == nil || reject.Code != isolation.CodeSocketPathChanged {
		t.Fatalf("repointed grant = grants %+v reject %+v, want %s", got, reject, isolation.CodeSocketPathChanged)
	}
}

// requireNonHTTPConfinement fails where the platform should confine but cannot.
func requireNonHTTPConfinement(t *testing.T) {
	t.Helper()
	if !confine.Available() {
		caps, _ := confine.LinuxCapabilities()
		t.Fatalf("confinement unavailable on %s: %+v", runtime.GOOS, caps)
	}
}
