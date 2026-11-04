package confine_test

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
)

// grantShapeFixture is one tree probed under every shape of grant.
type grantShapeFixture struct {
	home, control, project, data, outside string
}

func newGrantShapeFixture(t *testing.T) grantShapeFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve fixture root", err)
	f := grantShapeFixture{
		home:    filepath.Join(root, "home"),
		project: filepath.Join(root, "project"),
		outside: outsideTemporaryWriteRoots(t),
	}
	f.control = filepath.Join(root, "state", "paintedwolf")
	f.data = filepath.Join(f.project, "data")
	t.Setenv("HOME", f.home)
	t.Setenv("LYCAON_CONFIG_DIR", f.control)
	t.Setenv("LYCAON_SANDBOX_DENY_READ", "")
	confine.SetCredentialStorePathsSource(func() []string { return []string{f.data, "~/.aws/", "~/.docker/config.json"} })
	t.Cleanup(func() { confine.SetCredentialStorePathsSource(nil) })
	return f
}

// probes lists every path each shape is asked about. A path that exists at
// launch and one that does not are held by different mechanisms on Linux.
func (f grantShapeFixture) probes(t *testing.T) []boundaryProbe {
	t.Helper()
	var probes []boundaryProbe
	existing := []string{
		filepath.Join(f.data, "certificate.pem"),
		filepath.Join(f.data, "nested", "credentials"),
		filepath.Join(f.project, "src", "main.go"),
		filepath.Join(f.project, "AGENTS.md"),
		filepath.Join(f.project, settingsoverlay.DirName(), "settings.yaml"),
		filepath.Join(f.control, "api.token"),
		filepath.Join(f.control, "drafts", "d1", "note.md"),
		filepath.Join(f.home, ".ssh", "known_hosts"),
		filepath.Join(f.home, ".aws", "credentials"),
		filepath.Join(f.home, ".docker", "config.json"),
		filepath.Join(f.home, ".docker", "buildx", "activity"),
		filepath.Join(f.outside, "existing.txt"),
	}
	for _, path := range existing {
		writeFixtureFile(t, path)
		probes = append(probes,
			boundaryProbe{name: "read " + path, access: confine.AccessRead, path: path, exists: true},
			boundaryProbe{name: "write " + path, access: confine.AccessWrite, path: path, exists: true},
		)
	}
	for _, path := range []string{
		filepath.Join(f.data, "absent.pem"),
		filepath.Join(f.project, "src", "absent.go"),
		filepath.Join(f.project, "nested", "AGENTS.md"),
		filepath.Join(f.control, "drafts", "d1", "absent.md"),
		filepath.Join(f.control, "absent.token"),
		filepath.Join(f.outside, "absent.txt"),
	} {
		testutil.FailErr(t, "create probe parent", os.MkdirAll(filepath.Dir(path), 0o700))
		probes = append(probes, boundaryProbe{name: "create " + path, access: confine.AccessWrite, path: path})
	}
	return probes
}

// shapes lists every kind of grant a confinement can carry over the fixture.
func (f grantShapeFixture) shapes() map[string]confine.Confinement {
	grant := func(paths ...string) []confine.ProtectedPathGrant {
		var grants []confine.ProtectedPathGrant
		for _, path := range paths {
			grants = append(grants, confine.NewProtectedPathGrant(path))
		}
		return grants
	}
	certificate := filepath.Join(f.data, "certificate.pem")
	draft := filepath.Join(f.control, "drafts", "d1")
	base := func() confine.Confinement { return confine.Confinement{Roots: []string{f.project}} }
	shape := func(change func(*confine.Confinement)) confine.Confinement {
		c := base()
		change(&c)
		return c
	}
	return map[string]confine.Confinement{
		"no grant":    base(),
		"read denied": shape(func(c *confine.Confinement) { c.ReadDenyPaths = []string{f.data} }),
		"read grant of a file": shape(func(c *confine.Confinement) {
			c.ReadDenyPaths = []string{f.data}
			c.ProtectedReadGrants = grant(certificate)
		}),
		"read grant at the denial": shape(func(c *confine.Confinement) {
			c.ReadDenyPaths = []string{f.data}
			c.ProtectedReadGrants = grant(f.data)
		}),
		"read grant above the denial": shape(func(c *confine.Confinement) {
			c.ReadDenyPaths = []string{f.data}
			c.ProtectedReadGrants = grant(f.project)
		}),
		"read root above the denial": shape(func(c *confine.Confinement) { c.ReadDenyPaths = []string{f.data}; c.ReadRoots = []string{f.project} }),
		"read root above the floors": shape(func(c *confine.Confinement) { c.ReadRoots = []string{filepath.Dir(f.home)} }),
		"read root beside the denial": shape(func(c *confine.Confinement) {
			c.ReadDenyPaths = []string{f.data}
			c.ReadRoots = []string{filepath.Join(f.data, "nested")}
		}),
		"write grant of a file":       shape(func(c *confine.Confinement) { c.ProtectedWriteGrants = grant(certificate) }),
		"write grant at the floor":    shape(func(c *confine.Confinement) { c.ProtectedWriteGrants = grant(f.data) }),
		"write grant above the floor": shape(func(c *confine.Confinement) { c.ProtectedWriteGrants = grant(f.project) }),
		"reviewed root":               shape(func(c *confine.Confinement) { c.GrantedWriteRoots = []string{f.project} }),
		"reviewed and protected": shape(func(c *confine.Confinement) {
			c.GrantedWriteRoots = []string{f.project}
			c.ProtectedWriteGrants = grant(f.data)
		}),
		"reviewed root outside":  shape(func(c *confine.Confinement) { c.GrantedWriteRoots = []string{f.outside} }),
		"reviewed home":          shape(func(c *confine.Confinement) { c.GrantedWriteRoots = []string{f.home} }),
		"workspace in the state": shape(func(c *confine.Confinement) { c.GrantedWriteRoots = []string{draft} }),
		"policy file grant": shape(func(c *confine.Confinement) {
			c.PolicyWriteGrants = grant(filepath.Join(f.project, "AGENTS.md"))
		}),
		"policy grant under a protected grant": shape(func(c *confine.Confinement) {
			c.ProtectedWriteGrants = grant(f.project)
			c.PolicyWriteGrants = grant(filepath.Join(f.project, "AGENTS.md"))
		}),
	}
}

// The kernel and the host verdict must agree under every shape of grant: the
// rules are last-match, and a plan compiled differently would refuse what the
// host reported allowed, or allow what it reported refused.
func TestKernelAgreesWithVerdictUnderEveryGrantShape(t *testing.T) {
	helper := requireLiveBoundary(t)
	f := newGrantShapeFixture(t)
	probes := f.probes(t)
	for name, c := range f.shapes() {
		t.Run(name, func(t *testing.T) {
			c.Roots = liveBoundaryRoots(c.Roots...)
			rules := confine.BoundaryOf(&c).Filesystem
			for _, probe := range probes {
				verdict := rules.Verdict(probe.access, probe.path)
				if allowed := kernelPermits(t, helper, c, probe); allowed != verdict.Allowed {
					_, out := confinedRun(t, helper, c, "/bin/ls", "-l", probe.path)
					t.Errorf("%s: kernel allowed=%v, verdict=%+v: %s", probe.name, allowed, verdict, out)
				}
			}
		})
	}
}

// A managed read root reopens what is denied above it, never what is denied
// below it: that takes an approved read grant.
func TestReadRootDoesNotLiftTheDenialsBeneathIt(t *testing.T) {
	f := newGrantShapeFixture(t)
	secret := filepath.Join(f.data, "certificate.pem")
	token := filepath.Join(f.control, "api.token")
	key := filepath.Join(f.home, ".ssh", "known_hosts")
	c := confine.Confinement{Roots: []string{f.project}, ReadDenyPaths: []string{f.data}, ReadRoots: []string{filepath.Dir(f.home)}}
	rules := confine.BoundaryOf(&c).Filesystem
	for _, denied := range []string{secret, token, key} {
		if got := rules.Verdict(confine.AccessRead, denied); got.Allowed {
			t.Errorf("read root above %s reopened it", denied)
		}
	}
	if got := rules.Verdict(confine.AccessRead, filepath.Join(f.project, "src", "main.go")); !got.Allowed {
		t.Errorf("read root refused an ordinary file: %+v", got)
	}
	c.ProtectedReadGrants = []confine.ProtectedPathGrant{confine.NewProtectedPathGrant(f.project)}
	if got := confine.BoundaryOf(&c).Filesystem.Verdict(confine.AccessRead, secret); !got.Allowed {
		t.Errorf("an approved directory grant did not lift the denial beneath it: %+v", got)
	}
}

// Extra write roots from the environment widen the boundary, not just its rules.
func TestBoundaryHonorsConfiguredWriteRoots(t *testing.T) {
	self := requireLiveBoundary(t)
	extra := outsideTemporaryWriteRoots(t)
	t.Setenv("LYCAON_SANDBOX_WRITE_ROOTS", extra)
	c := confine.Confinement{Roots: []string{t.TempDir()}}
	if code := confinedExit(t, self, c, "/usr/bin/touch", filepath.Join(extra, "ok")); code != 0 {
		t.Fatalf("configured write root refused: exit=%d", code)
	}
}

// loopbackReaches reports which host ports a confined process connected to.
func loopbackReaches(t *testing.T, self string, c confine.Confinement, ports ...uint16) string {
	t.Helper()
	const probe = `
import socket, sys
for port in sys.argv[1:]:
    s = socket.socket()
    s.settimeout(2)
    try:
        s.connect(('127.0.0.1', int(port)))
        print(port, 'reached')
    except OSError:
        print(port, 'refused')
`
	args := []string{"-c", probe}
	for _, port := range ports {
		args = append(args, strconv.Itoa(int(port)))
	}
	code, out, stderr := confinedStdout(t, self, c, requirePython(t), args...)
	if code != 0 {
		t.Fatalf("loopback probe exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	return out
}

func hostListenerPort(t *testing.T) uint16 {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	testutil.FailErr(t, "listen", err)
	t.Cleanup(func() { _ = ln.Close() })
	return uint16(ln.Addr().(*net.TCPAddr).Port)
}

// A loopback grant reaches its ports and no others, in every profile carrying one.
func TestBoundaryLoopbackGrantFollowsItsPorts(t *testing.T) {
	self := requireLiveBoundary(t)
	granted, other := hostListenerPort(t), hostListenerPort(t)
	narrowed := func(browser bool) confine.Confinement {
		return confine.Confinement{
			Roots: []string{t.TempDir()}, Network: confine.NetworkDeny, Browser: browser,
			LoopbackConnect: true, LoopbackConnectPorts: []uint16{granted},
		}
	}
	want := fmt.Sprintf("%d reached\n%d refused\n", granted, other)
	if got := loopbackReaches(t, self, narrowed(false), granted, other); got != want {
		t.Errorf("narrowed grant:\ngot:\n%swant:\n%s", got, want)
	}
	if got := loopbackReaches(t, self, narrowed(true), granted, other); got != want {
		t.Errorf("narrowed browser:\ngot:\n%swant:\n%s", got, want)
	}
	floor := narrowed(true)
	floor.LoopbackConnectPorts = nil
	if got, want := loopbackReaches(t, self, floor, granted, other), fmt.Sprintf("%d reached\n%d reached\n", granted, other); got != want {
		t.Errorf("browser floor:\ngot:\n%swant:\n%s", got, want)
	}
	none := narrowed(false)
	none.LoopbackConnect, none.LoopbackConnectPorts = false, nil
	if got, want := loopbackReaches(t, self, none, granted, other), fmt.Sprintf("%d refused\n%d refused\n", granted, other); got != want {
		t.Errorf("no grant:\ngot:\n%swant:\n%s", got, want)
	}
}

// freeLoopbackPort finds a port nothing is listening on.
func freeLoopbackPort(t *testing.T) uint16 {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	testutil.FailErr(t, "listen", err)
	port := uint16(ln.Addr().(*net.TCPAddr).Port)
	testutil.FailErr(t, "release port", ln.Close())
	return port
}

// confinedListenerReachable reports whether the host reaches a port a confined
// process tried to listen on.
func confinedListenerReachable(t *testing.T, self string, c confine.Confinement, port uint16) bool {
	t.Helper()
	const probe = `
import socket, sys, time
s = socket.socket()
s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
try:
    s.bind(('127.0.0.1', int(sys.argv[1])))
    s.listen(1)
    print('BOUND', flush=True)
except OSError as e:
    print('DENIED', e, flush=True)
    sys.exit(0)
time.sleep(30)
`
	c.Roots = liveBoundaryRoots(c.Roots...)
	cmd, cleanup, err := confine.Command(t.Context(), self, requirePython(t), []string{"-c", probe, strconv.Itoa(int(port))}, c)
	testutil.FailErr(t, "confine.Command", err)
	defer cleanup()
	cmd.WaitDelay = 5 * time.Second
	stdout, err := cmd.StdoutPipe()
	testutil.FailErr(t, "stdout", err)
	testutil.FailErr(t, "start listener", cmd.Start())
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	line, err := bufio.NewReader(stdout).ReadString('\n')
	testutil.FailErr(t, "read listener status", err)
	if !strings.HasPrefix(line, "BOUND") {
		return false
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port))), 2*time.Second)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// A listen grant serves the host on its ports only. A listener without a grant
// is refused on macOS and stays inside the sandbox on Linux; the host reaches
// neither.
func TestBoundaryListenGrantFollowsItsPorts(t *testing.T) {
	self := requireLiveBoundary(t)
	granted, other := freeLoopbackPort(t), freeLoopbackPort(t)
	c := confine.Confinement{
		Roots: []string{t.TempDir()}, Network: confine.NetworkDeny,
		LocalListen: true, LocalListenPorts: []uint16{granted},
	}
	if !confinedListenerReachable(t, self, c, granted) {
		t.Errorf("the host did not reach the granted port %d", granted)
	}
	if confinedListenerReachable(t, self, c, other) {
		t.Errorf("the host reached ungranted port %d", other)
	}
	ungranted := confine.Confinement{Roots: []string{t.TempDir()}, Network: confine.NetworkDeny}
	if confinedListenerReachable(t, self, ungranted, granted) {
		t.Errorf("the host reached a listener without a grant on %d", granted)
	}
}
