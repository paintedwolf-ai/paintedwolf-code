package confine_test

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestDefaultConfinementRejectsSocketConflictAndCap(t *testing.T) {
	confine.TestingSetAutoConfine(t)
	dir := shortTempDir(t)
	a := filepath.Join(dir, "a.sock")
	ga := socketGrant(t, a)

	c, ok := confine.DefaultConfinement(confine.Request{
		Roots: []string{dir},
		SocketGrants: []confine.SocketGrant{
			ga,
			{ApprovedPath: a, ResolvedPath: filepath.Join(dir, "other.sock")},
		},
	})
	if ok || c != nil {
		t.Fatalf("conflicting grants must reject request: ok=%v c=%v", ok, c)
	}

	grants := make([]confine.SocketGrant, 0, confine.MaxSocketGrants+1)
	for i := 0; i < confine.MaxSocketGrants+1; i++ {
		grants = append(grants, socketGrant(t, filepath.Join(dir, fmt.Sprintf("s%d.sock", i))))
	}
	c, ok = confine.DefaultConfinement(confine.Request{Roots: []string{dir}, SocketGrants: grants})
	if ok || c != nil {
		t.Fatalf("over-cap grants must reject request: ok=%v", ok)
	}
}

func TestBuildProfileExactSocketLiterals(t *testing.T) {
	dir := shortTempDir(t)
	sock := socketGrant(t, filepath.Join(dir, "svc.sock"))
	wild := socketGrant(t, filepath.Join(dir, "star*.sock"))

	p, err := confine.BuildProfile(confine.Confinement{
		Roots:        []string{dir},
		Network:      confine.NetworkDeny,
		SocketGrants: []confine.SocketGrant{sock, wild},
	})
	testutil.FailErr(t, "BuildProfile", err)
	if !strings.Contains(p, "; exact AF_UNIX socket grants") {
		t.Fatalf("socket block marker missing:\n%s", p)
	}
	wantLit := `(allow network-outbound (literal "` + sock.ResolvedPath + `"))`
	if !strings.Contains(p, wantLit) {
		t.Fatalf("missing exact literal:\n%s", p)
	}
	if !strings.Contains(p, `star*.sock`) {
		t.Fatalf("wildcard filename must appear as literal path:\n%s", p)
	}
	if strings.Contains(p, "(allow network*)") {
		t.Fatalf("must not emit network*:\n%s", p)
	}
	if strings.Contains(p, "(allow network-outbound (subpath") {
		t.Fatalf("socket grants must not use subpath:\n%s", p)
	}
}

func TestBuildProfileDropsRepointedSocketGrant(t *testing.T) {
	dir := shortTempDir(t)
	a := filepath.Join(dir, "a.sock")
	b := filepath.Join(dir, "b.sock")
	listenUnix(t, a)
	listenUnix(t, b)
	link := filepath.Join(dir, "link.sock")
	testutil.FailErr(t, "symlink", os.Symlink(a, link))
	resolved, err := filepath.EvalSymlinks(link)
	testutil.FailErr(t, "eval", err)

	testutil.FailErr(t, "remove link", os.Remove(link))
	testutil.FailErr(t, "repoint", os.Symlink(b, link))

	p, err := confine.BuildProfile(confine.Confinement{
		Roots: []string{dir},
		SocketGrants: []confine.SocketGrant{
			{ApprovedPath: link, ResolvedPath: resolved},
		},
	})
	testutil.FailErr(t, "BuildProfile", err)
	if strings.Contains(p, `(literal "`+resolved+`")`) {
		t.Fatalf("repointed grant must be dropped:\n%s", p)
	}
	bResolved, _ := filepath.EvalSymlinks(b)
	if bResolved != "" && strings.Contains(p, `(literal "`+bResolved+`")`) {
		t.Fatalf("must not silently redirect to new target:\n%s", p)
	}
}

func TestSocketPathsDigestStable(t *testing.T) {
	g1 := []confine.SocketGrant{
		{ApprovedPath: "/b", ResolvedPath: "/b"},
		{ApprovedPath: "/a", ResolvedPath: "/a"},
	}
	g2 := []confine.SocketGrant{
		{ApprovedPath: "/a", ResolvedPath: "/a"},
		{ApprovedPath: "/b", ResolvedPath: "/b"},
	}
	if confine.SocketPathsDigest(g1) == "" || confine.SocketPathsDigest(g1) != confine.SocketPathsDigest(g2) {
		t.Fatalf("digest must be order-stable: %q vs %q", confine.SocketPathsDigest(g1), confine.SocketPathsDigest(g2))
	}
	g3 := []confine.SocketGrant{{ApprovedPath: "/a", ResolvedPath: "/other"}}
	if confine.SocketPathsDigest(g1) == confine.SocketPathsDigest(g3) {
		t.Fatal("different resolved path must change digest")
	}
}

func TestFailClosedBrokerStartup(t *testing.T) {
	confine.TestingSetAutoConfine(t)
	t.Setenv("LYCAON_BYPASS_APPROVALS", "")
	t.Setenv("LYCAON_SANDBOX", "")
	t.Setenv("LYCAON_SANDBOX_NETWORK", "")
	t.Cleanup(confine.SetBrokerUnavailableForTest(os.ErrInvalid))

	c, ok := confine.DefaultConfinement(confine.Request{Roots: []string{t.TempDir()}})
	if !confine.Available() {
		t.Skip("confine unavailable on this platform")
	}
	if !ok || c == nil {
		t.Fatal("confinement must still apply under broker failure")
	}
	if _, err := confine.BindAction(c, confine.EgressCommand{ToolCallID: "failure-test"}); err == nil {
		t.Fatal("broker failure must refuse the action lease")
	}
	deg, yes := confine.EgressDegradedState()
	if !yes || deg.Reason == "" {
		t.Fatalf("degraded state missing: yes=%v deg=%+v", yes, deg)
	}
}

func TestDirectIPRequestSelectsDirectMode(t *testing.T) {
	confine.TestingSetAutoConfine(t)
	t.Setenv("LYCAON_BYPASS_APPROVALS", "")
	t.Setenv("LYCAON_SANDBOX", "")
	t.Setenv("LYCAON_SANDBOX_NETWORK", "")
	if !confine.Available() {
		t.Skip("confine unavailable on this platform")
	}
	c, ok := confine.DefaultConfinement(confine.Request{
		Roots:  []string{t.TempDir()},
		Egress: confine.EgressDirectIP,
	})
	if !ok || c == nil || c.Network != confine.NetworkDirectIP {
		t.Fatalf("EgressDirectIP must select NetworkDirectIP: ok=%v c=%+v", ok, c)
	}
	if c.ProxyAddr != "" || c.LineageID != "" {
		t.Fatalf("direct IP must not wire a mediation address: %+v", c)
	}
	if confine.NetworkLabel(c.Network) != "direct_ip" {
		t.Fatalf("NetworkLabel=%q", confine.NetworkLabel(c.Network))
	}
}

// shortTempDir stays within the socket path limit.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "lyc-sb-") //nolint:usetesting // Short socket path.
	testutil.FailErr(t, "mkdir temp", err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func socketGrant(t *testing.T, path string) confine.SocketGrant {
	t.Helper()
	listenUnix(t, path)
	resolved, err := filepath.EvalSymlinks(path)
	testutil.FailErr(t, "EvalSymlinks", err)
	return confine.SocketGrant{ApprovedPath: path, ResolvedPath: resolved}
}

func listenUnix(t *testing.T, path string) net.Listener {
	t.Helper()
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	testutil.FailErr(t, "listen unix", err)
	t.Cleanup(func() {
		_ = ln.Close()
		_ = os.Remove(path)
	})
	return ln
}
