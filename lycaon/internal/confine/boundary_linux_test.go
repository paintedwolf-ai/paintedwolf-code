//go:build linux

package confine

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/confine/linuxsandbox"
	"github.com/lycaon/lycaon/internal/protectedpath"
	"github.com/lycaon/lycaon/internal/testutil"
)

// Denials inside a grant become mounts; denials Landlock already refuses do not.
func TestCompileFilesystemPlanMasksDenialsInsideGrants(t *testing.T) {
	proj := t.TempDir()
	ro := filepath.Join(proj, "vendor")
	testutil.FailErr(t, "mkdir vendor", os.Mkdir(ro, 0o755))
	cfg := t.TempDir()
	ws := filepath.Join(cfg, "workspaces", "w1")
	testutil.FailErr(t, "mkdir workspace", os.MkdirAll(ws, 0o755))

	var rules FilesystemRules
	rules.add(fsRule{op: opRead, allow: true, everywhere: true})
	rules.add(fsRule{op: opRead, matches: subpathMatches([]string{cfg}, FloorReadControlPlane)})
	rules.add(fsRule{op: opRead, allow: true, matches: subpathMatches([]string{ws}, "")})
	rules.add(fsRule{op: opReadMetadata, allow: true, matches: literalMatches([]string{cfg}, "")})
	rules.add(fsRule{op: opWrite, allow: true, matches: append(subpathMatches([]string{proj}, ""),
		fsMatch{literal: "/dev/null"}, fsMatch{subpath: "/dev/fd"}, fsMatch{regex: "^/dev/ttys"})})
	rules.add(fsRule{op: opWrite, matches: []fsMatch{
		{subpath: ro, layer: FloorBaseline},
		{subpath: "/nowhere/protected", layer: FloorProtected},
		{subpath: cfg, layer: FloorControlPlane, except: []string{ws}},
	}})
	rules.add(fsRule{op: opWrite, allow: true, matches: subpathMatches([]string{ws}, "")})
	rules.add(fsRule{op: opWrite, matches: []fsMatch{{regex: "^/dev/disk", layer: FloorControlPlane}}})

	fs, err := compileFilesystemPlan(rules)
	testutil.FailErr(t, "compile", err)
	want := linuxsandbox.Filesystem{
		WriteRoots: []string{proj, ws},
		ReadOnly:   []string{ro},
		Hidden:     []string{cfg},
		Restored:   []string{ws},
	}
	if got := sortedPlan(fs); !reflect.DeepEqual(got, want) {
		t.Fatalf("plan = %+v\nwant %+v", got, want)
	}
}

// A later grant for a masked path lifts the mask instead of restoring beneath it.
func TestCompileFilesystemPlanLiftsMaskForLaterGrant(t *testing.T) {
	proj := t.TempDir()
	inner := filepath.Join(proj, "docs")
	testutil.FailErr(t, "mkdir docs", os.Mkdir(inner, 0o755))
	var rules FilesystemRules
	rules.add(fsRule{op: opWrite, allow: true, matches: subpathMatches([]string{proj}, "")})
	rules.add(fsRule{op: opWrite, matches: subpathMatches([]string{inner}, FloorBaseline)})
	rules.add(fsRule{op: opWrite, allow: true, matches: subpathMatches([]string{inner}, "")})
	fs, err := compileFilesystemPlan(rules)
	testutil.FailErr(t, "compile", err)
	if len(fs.ReadOnly) != 0 || len(fs.Restored) != 0 {
		t.Fatalf("lifted mask left mounts: %+v", fs)
	}
	if got := sortedPlan(fs).WriteRoots; !reflect.DeepEqual(got, []string{inner, proj}) {
		t.Fatalf("write roots = %v", got)
	}
}

// Loader files named at any depth are masked where they exist at launch.
func TestCompileFilesystemPlanNamesLoaderFilesAtAnyDepth(t *testing.T) {
	proj := t.TempDir()
	top := filepath.Join(proj, protectedpath.AgentsMDFileName)
	nested := filepath.Join(proj, "pkg", "agents.md")
	other := filepath.Join(proj, "pkg", "README.md")
	testutil.FailErr(t, "mkdir pkg", os.Mkdir(filepath.Dir(nested), 0o755))
	for _, p := range []string{top, nested, other} {
		testutil.FailErr(t, "write "+p, os.WriteFile(p, []byte("x"), 0o644))
	}
	var location protectedpath.AgentPolicyLocation
	for _, l := range protectedpath.AgentPolicyLocations() {
		if l.Name == protectedpath.AgentsMDFileName {
			location = l
		}
	}
	var rules FilesystemRules
	rules.add(fsRule{op: opWrite, allow: true, matches: subpathMatches([]string{proj}, "")})
	rules.add(fsRule{op: opWrite, matches: []fsMatch{{subpath: proj, regex: location.NameRegex(), layer: FloorAgentPolicy}}})
	fs, err := compileFilesystemPlan(rules)
	testutil.FailErr(t, "compile", err)
	if got := sortedPlan(fs).ReadOnly; !reflect.DeepEqual(got, []string{top, nested}) {
		t.Fatalf("read-only = %v, want both loader files", got)
	}

	rules = FilesystemRules{}
	rules.add(fsRule{op: opWrite, allow: true, matches: subpathMatches([]string{proj}, "")})
	rules.add(fsRule{op: opWrite, matches: []fsMatch{{subpath: proj, regex: "^.*\\.secret$", layer: FloorBaseline}}})
	_, err = compileFilesystemPlan(rules)
	var unsupported *linuxsandbox.Unsupported
	if !errors.As(err, &unsupported) || unsupported.Code != linuxsandbox.UnsupportedFilesystemRule {
		t.Fatalf("unknown pattern: %v", err)
	}
}

// The rendered plan carries the engine's namespaces and refuses modes the
// helper cannot express.
func TestRenderProfileBuildsADenyPlanAndRefusesMediatedEgress(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	proj := t.TempDir()
	raw, err := renderProfile(Confinement{Roots: []string{proj}, Network: NetworkDeny, EgressOff: EgressOffOverride})
	testutil.FailErr(t, "render deny plan", err)
	plan, err := linuxsandbox.DecodePlan([]byte(raw))
	testutil.FailErr(t, "decode", err)
	for _, kind := range linuxsandbox.NamespaceKinds() {
		if plan.Attest.HostNamespaces[kind] == "" {
			t.Fatalf("plan lacks host namespace %s: %+v", kind, plan.Attest)
		}
	}
	if !containsPath(plan.Filesystem.WriteRoots, proj) {
		t.Fatalf("plan write roots %v lack the project", plan.Filesystem.WriteRoots)
	}
	if plan.Network.Mode != linuxsandbox.NetworkDeny {
		t.Fatalf("network mode %q", plan.Network.Mode)
	}

	_, err = renderProfile(Confinement{Roots: []string{proj}, Network: NetworkProxyOnly, ProxyAddr: "127.0.0.1:1"})
	var unsupported *linuxsandbox.Unsupported
	if !errors.As(err, &unsupported) || unsupported.Code != linuxsandbox.UnsupportedNetworkMode {
		t.Fatalf("proxy-only plan: %v", err)
	}
	_, err = renderProfile(Confinement{Roots: []string{proj}, Network: NetworkDeny, Browser: true, LoopbackConnect: true})
	if !errors.As(err, &unsupported) || unsupported.Code != linuxsandbox.UnsupportedBrowser {
		t.Fatalf("browser plan: %v", err)
	}
}

func TestNetworkPlanMirrorsBoundaryDerivations(t *testing.T) {
	t.Parallel()
	direct := networkPlan(Confinement{Network: NetworkDirectIP, DirectIPPermits: []DirectIPPermit{{Protocol: "tcp", Port: 443}}})
	if !direct.Listen || !direct.LoopbackConnect || len(direct.DirectIPPermits) != 1 || direct.Mode != linuxsandbox.NetworkDirectIP {
		t.Fatalf("direct plan: %+v", direct)
	}
	deny := networkPlan(Confinement{Network: NetworkDeny, LocalListen: true, LocalListenPorts: []uint16{8080},
		SocketGrants: []SocketGrant{{ApprovedPath: "/run/a", ResolvedPath: "/run/a"}}})
	if deny.LoopbackConnect || !deny.Listen || !reflect.DeepEqual(deny.ListenPorts, []uint16{8080}) || !reflect.DeepEqual(deny.SocketPaths, []string{"/run/a"}) {
		t.Fatalf("deny plan: %+v", deny)
	}
}

func sortedPlan(fs linuxsandbox.Filesystem) linuxsandbox.Filesystem {
	plan := linuxsandbox.Plan{Version: linuxsandbox.PlanVersion, Filesystem: fs}
	raw, _ := plan.Encode()
	decoded, _ := linuxsandbox.DecodePlan(raw)
	return decoded.Filesystem
}

func containsPath(list []string, want string) bool {
	for _, p := range list {
		if PathEqual(p, want) {
			return true
		}
	}
	return false
}
