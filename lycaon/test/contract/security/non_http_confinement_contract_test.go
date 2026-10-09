package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hitl"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Confinement integrity for fail-closed mediation, emitters, Contained, and grants.

func TestNonHTTPContractProxyStartupFailureDeniesAndRollsBack(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	proxySrc := contractcheck.ReadRepoFile(t, root, "lycaon/internal/egressproxy/proxy.go")
	fn := mustFindFunc(t, proxySrc, "proxy.go", "Start")
	body := proxySrc[fn.Body.Pos()-1 : fn.Body.End()]
	if !strings.Contains(body, "httpLn.Close()") {
		t.Fatal("Start must close the HTTP listener when SOCKS bind fails")
	}

	confineSrc := contractcheck.ReadRepoFile(t, root, "lycaon/internal/confine/confine_action.go")
	bind := mustFindFunc(t, confineSrc, "confine_action.go", "BindAction")
	bindBody := confineSrc[bind.Body.Pos()-1 : bind.Body.End()]
	if !strings.Contains(bindBody, "brokerAddrs()") || !strings.Contains(bindBody, "line.Close()") {
		t.Fatal("BindAction must require a live front door and release the lineage on failure")
	}
	if !strings.Contains(bindBody, "lineage.Supported()") {
		t.Fatal("BindAction must refuse mediation where descendants cannot be observed")
	}

	confine.TestingSetAutoConfine(t)
	t.Setenv("LYCAON_BYPASS_APPROVALS", "")
	t.Setenv("LYCAON_SANDBOX", "")
	t.Setenv("LYCAON_SANDBOX_NETWORK", "")
	if !confine.Available() {
		t.Skip("confine unavailable on this platform")
	}
	t.Cleanup(confine.SetBrokerUnavailableForTest(os.ErrInvalid))
	c, ok := confine.DefaultConfinement(confine.Request{Roots: []string{t.TempDir()}})
	if !ok || c == nil || c.Network != confine.NetworkProxyOnly {
		t.Fatalf("default must still compile the proxy-only intent, got ok=%v c=%+v", ok, c)
	}
	if _, err := confine.BindAction(c, confine.EgressCommand{ToolCallID: "contract"}); err == nil {
		t.Fatal("broker failure must refuse the action before launch")
	}
}

func TestNonHTTPContractNoAmbientNetworkAllowAliasExtended(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	src := contractcheck.ReadRepoFile(t, root, "lycaon/internal/confine/confine.go")
	fn := mustFindFunc(t, src, "confine.go", "DefaultConfinement")
	body := src[fn.Body.Pos()-1 : fn.Body.End()]
	for _, alias := range []string{`case "allow"`, `case "unrestricted"`, `case "off"`} {
		if strings.Contains(body, alias) {
			t.Fatalf("DefaultConfinement must not special-case ambient alias %s", alias)
		}
	}
	// LYCAON_SANDBOX_NETWORK may still exist for deny|ask|lockdown posture, never ambient open.
	envSwitch := body
	if strings.Contains(envSwitch, `"allow"`) || strings.Contains(envSwitch, `"unrestricted"`) {
		t.Fatal("LYCAON_SANDBOX_NETWORK parse must not open network via allow/unrestricted")
	}
}

func TestNonHTTPContractSocketEmitterLiteralOnlyAndDirectRemoteIP(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	src := contractcheck.ReadRepoFile(t, root, "lycaon/internal/confine/network_profile.go")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "network_profile.go", src, 0)
	contractcheck.FailErr(t, "parse network_profile.go", err)

	sockFn := findNamedFunc(file, "writeSocketGrantRules")
	if sockFn == nil {
		t.Fatal("writeSocketGrantRules missing")
	}
	hasLiteral := false
	ast.Inspect(sockFn.Body, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		for _, banned := range []string{"subpath", "regex", "network*"} {
			if strings.Contains(lit.Value, banned) {
				t.Fatalf("writeSocketGrantRules must not emit %q", banned)
			}
		}
		if strings.Contains(lit.Value, "literal") {
			hasLiteral = true
		}
		return true
	})
	if !hasLiteral {
		t.Fatal("writeSocketGrantRules must emit literal predicates")
	}

	netFn := findNamedFunc(file, "writeNetworkRules")
	if netFn == nil {
		t.Fatal("writeNetworkRules missing")
	}
	hasRemoteIP := false
	ast.Inspect(netFn.Body, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		if strings.Contains(lit.Value, "(allow network*)") {
			t.Fatal("writeNetworkRules must not emit blanket network*")
		}
		if strings.Contains(lit.Value, "allow network-outbound") && !strings.Contains(lit.Value, "remote ip") && !strings.Contains(lit.Value, "literal") {
			t.Fatalf("forbidden bare network-outbound allow: %s", lit.Value)
		}
		if strings.Contains(lit.Value, "subpath") {
			t.Fatal("network rules must not use socket subpath grants")
		}
		if strings.Contains(lit.Value, "remote ip") {
			hasRemoteIP = true
		}
		return true
	})
	if !hasRemoteIP {
		t.Fatal("direct profile must emit remote-IP outbound predicate")
	}

	profile, err := confine.BuildProfile(confine.Confinement{Roots: []string{"/proj"}, Network: confine.NetworkDirectIP})
	contractcheck.FailErr(t, "BuildProfile direct", err)
	if !strings.Contains(profile, `(allow network-outbound (remote ip "*:*"))`) {
		t.Fatalf("direct profile missing remote IP allow:\n%s", profile)
	}
	if strings.Contains(profile, "(allow network*)") {
		t.Fatal("direct profile must not emit network*")
	}
}

func TestNonHTTPContractEgressContainedExcludesDirectAndBypass(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	src := contractcheck.ReadRepoFile(t, root, "lycaon/internal/settings/reversibility.go")
	fn := mustFindFunc(t, src, "reversibility.go", "boundaryHolds")
	body := src[fn.Body.Pos()-1 : fn.Body.End()]
	if strings.Contains(body, "DirectIP") || strings.Contains(body, "direct_ip") {
		t.Fatal("boundaryHolds must not treat direct_ip as a held boundary")
	}
	if !strings.Contains(body, "ContainedEgressDeny") || !strings.Contains(body, "ContainedEgressProxy") {
		t.Fatal("boundaryHolds true set must be deny|proxy only")
	}

	if settingsEgressContained(hitl.Contained{FSJailed: true, Egress: hitl.ContainedEgressDirectIP}) {
		t.Fatal("direct_ip must not be egress-contained")
	}
	if settingsEgressContained(hitl.Contained{}) {
		t.Fatal("bypass/zero Contained must not be egress-contained")
	}
	if !settingsEgressContained(hitl.Contained{FSJailed: true, Egress: hitl.ContainedEgressProxy}) {
		t.Fatal("proxy+FSJailed must be egress-contained")
	}
}

func TestNonHTTPContractContainedDerivesFromSameRequestAsProfile(t *testing.T) {
	dir := shortUnixTempDir(t)
	sockPath := filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", sockPath)
	contractcheck.FailErr(t, "listen unix", err)
	t.Cleanup(func() { _ = ln.Close() })
	grant, err := confine.ResolveSocketRequest(sockPath)
	contractcheck.FailErr(t, "ResolveSocketRequest", err)

	// ContainedFromConfinement is the shared projection used by gate + executor.
	synthetic := &confine.Confinement{
		Roots:        []string{dir},
		Network:      confine.NetworkDirectIP,
		SocketGrants: []confine.SocketGrant{grant},
	}
	fromConf := hitl.ContainedFromConfinement(synthetic, true)
	if fromConf.SocketCount != 1 || fromConf.SocketPathsDigest == "" || !fromConf.DirectIP {
		t.Fatalf("Contained must project socket digest/count and DirectIP: %+v", fromConf)
	}
	if fromConf.Egress != hitl.ContainedEgressDirectIP {
		t.Fatalf("Contained.Egress=%q want direct_ip", fromConf.Egress)
	}

	confine.TestingSetAutoConfine(t)
	t.Setenv("LYCAON_BYPASS_APPROVALS", "")
	t.Setenv("LYCAON_SANDBOX", "")
	t.Setenv("LYCAON_SANDBOX_NETWORK", "")
	if !confine.Available() {
		return
	}
	req := confine.Request{
		Roots:        []string{dir},
		SocketGrants: []confine.SocketGrant{grant},
		Egress:       confine.EgressDirectIP,
	}
	conf, ok := confine.DefaultConfinement(req)
	if !ok || conf == nil {
		t.Fatal("DefaultConfinement must succeed for prepared request")
	}
	fromReq := hitl.ContainedForRequest(req)
	fromSame := hitl.ContainedFromConfinement(conf, true)
	if fromReq.SocketCount != fromSame.SocketCount || fromReq.SocketPathsDigest != fromSame.SocketPathsDigest {
		t.Fatalf("ContainedForRequest vs ContainedFromConfinement socket drift: %+v vs %+v", fromReq, fromSame)
	}
	if fromReq.DirectIP != fromSame.DirectIP || fromReq.Egress != fromSame.Egress {
		t.Fatalf("Contained egress/direct drift: %+v vs %+v", fromReq, fromSame)
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
		Tool:       "command",
		ProjectDir: "/proj",
		Args:       map[string]any{"command": "true"},
		Contained: hitl.Contained{
			FSJailed: true,
			Egress:   hitl.ContainedEgressProxy,
			Roots:    []string{"/proj"},
		},
	}
	k0 := hitl.GrantKey(base)

	withRoot := base
	withRoot.Contained.Roots = []string{"/proj", "/other"}
	if hitl.GrantKey(withRoot) == k0 {
		t.Fatal("roots must change GrantKey")
	}

	withSock := base
	withSock.Contained.SocketPathsDigest = "sock-a"
	withSock.Contained.SocketCount = 1
	if hitl.GrantKey(withSock) != k0 {
		t.Fatal("socket overlay changed GrantKey")
	}

	direct := base
	direct.Contained.Egress = hitl.ContainedEgressDirectIP
	direct.Contained.DirectIP = true
	if hitl.GrantKey(direct) == k0 {
		t.Fatal("direct IP must change GrantKey")
	}

	mediated := base
	mediated.Contained.Egress = hitl.ContainedEgressDeny
	if hitl.GrantKey(mediated) == k0 {
		t.Fatal("mediated egress label must change GrantKey")
	}

	witnessSrc := contractcheck.ReadRepoFile(t, contractcheck.RepoRoot(t), "lycaon/internal/capabilitygrants/socket_execution_grants.go")
	wfn := mustFindFunc(t, witnessSrc, "socket_execution_grants.go", "socketChatGrantOffer")
	wbody := witnessSrc[wfn.Body.Pos()-1 : wfn.Body.End()]
	if !strings.Contains(wbody, "BoundaryWitness") {
		t.Fatal("socket grant must use BoundaryWitness")
	}
}

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

	grant, err := confine.ResolveSocketRequest(link)
	contractcheck.FailErr(t, "ResolveSocketRequest", err)
	if err := confine.RevalidateSocketGrant(grant); err != nil {
		t.Fatalf("fresh grant must revalidate: %v", err)
	}

	contractcheck.FailErr(t, "remove link", os.Remove(link))
	contractcheck.FailErr(t, "repoint symlink", os.Symlink(realB, link))
	if err := confine.RevalidateSocketGrant(grant); err == nil {
		t.Fatal("repointed symlink must fail RevalidateSocketGrant")
	}

	spawn := contractcheck.ReadRepoFile(t, contractcheck.RepoRoot(t), "lycaon/internal/tools/socket_spawn.go")
	fin := mustFindFunc(t, spawn, "socket_spawn.go", "FinalizeSocketGrantsForSpawn")
	body := spawn[fin.Body.Pos()-1 : fin.Body.End()]
	if !strings.Contains(body, "RevalidateSocketGrant") {
		t.Fatal("spawn finalize must revalidate socket grants before apply")
	}
}

// settingsEgressContained mirrors rule_gate.egressContained polarity without exporting it.
func settingsEgressContained(c hitl.Contained) bool {
	return c.FSJailed && (c.Egress == hitl.ContainedEgressDeny || c.Egress == hitl.ContainedEgressProxy)
}
