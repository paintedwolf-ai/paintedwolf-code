package confine_test

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/testutil"
)

// Local socket grants use exact literals.
func TestSeatbeltSocketGrantMatrix(t *testing.T) {
	self := requireSeatbelt(t)
	dir := shortTempDir(t)
	aPath := filepath.Join(dir, "a.sock")
	bPath := filepath.Join(dir, "b.sock")
	listenUnix(t, aPath)
	listenUnix(t, bPath)
	link := filepath.Join(dir, "link.sock")
	testutil.FailErr(t, "symlink a", os.Symlink(aPath, link))
	resolvedA, err := filepath.EvalSymlinks(link)
	testutil.FailErr(t, "eval link", err)

	proj := filepath.Join(dir, "proj")
	testutil.FailErr(t, "mkdir proj", os.Mkdir(proj, 0o755))

	defaultC := confine.Confinement{Roots: []string{proj}, Network: confine.NetworkDeny}
	if code := unixConnectExit(t, self, defaultC, aPath); code == 0 {
		t.Fatal("default profile must deny AF_UNIX under a writable root")
	}
	if code := unixConnectExit(t, self, defaultC, bPath); code == 0 {
		t.Fatal("default profile must deny sibling AF_UNIX")
	}

	resolvedAPath, err := filepath.EvalSymlinks(aPath)
	testutil.FailErr(t, "eval a", err)
	grantA := confine.Confinement{
		Roots:   []string{proj},
		Network: confine.NetworkDeny,
		SocketGrants: []confine.SocketGrant{
			{ApprovedPath: aPath, ResolvedPath: resolvedAPath},
		},
	}
	if code := unixConnectExit(t, self, grantA, aPath); code != 0 {
		t.Fatalf("literal A must connect: exit=%d", code)
	}
	if code := unixConnectExit(t, self, grantA, bPath); code == 0 {
		t.Fatal("sibling B must stay denied")
	}

	grantLink := confine.Confinement{
		Roots:   []string{proj},
		Network: confine.NetworkDeny,
		SocketGrants: []confine.SocketGrant{
			{ApprovedPath: link, ResolvedPath: resolvedA},
		},
	}
	if code := unixConnectExit(t, self, grantLink, aPath); code != 0 {
		t.Fatalf("approved symlink resolving to A must connect canonical target: exit=%d", code)
	}

	// Repointed grants fail closed.
	testutil.FailErr(t, "remove link", os.Remove(link))
	testutil.FailErr(t, "repoint to B", os.Symlink(bPath, link))
	repointed := confine.Confinement{
		Roots:   []string{proj},
		Network: confine.NetworkDeny,
		SocketGrants: []confine.SocketGrant{
			{ApprovedPath: link, ResolvedPath: resolvedA},
		},
	}
	if code := unixConnectExit(t, self, repointed, aPath); code == 0 {
		t.Fatal("repointed grant must not keep A")
	}
	if code := unixConnectExit(t, self, repointed, bPath); code == 0 {
		t.Fatal("repointed grant must not redirect to B")
	}

	// A socket grant does not expose sibling files.
	home := shortTempDir(t)
	deniedParent := filepath.Join(home, ".secret-store")
	testutil.FailErr(t, "mkdir denied parent", os.MkdirAll(deniedParent, 0o755))
	secretSock := filepath.Join(deniedParent, "runtime.sock")
	listenUnix(t, secretSock)
	siblingFile := filepath.Join(deniedParent, "creds")
	testutil.FailErr(t, "write sibling", os.WriteFile(siblingFile, []byte("secret"), 0o600))
	t.Setenv("LYCAON_SANDBOX_DENY_READ", deniedParent)
	secretResolved, err := filepath.EvalSymlinks(secretSock)
	testutil.FailErr(t, "eval secret sock", err)
	secretGrant := confine.Confinement{
		Roots:   []string{proj},
		Network: confine.NetworkDeny,
		SocketGrants: []confine.SocketGrant{
			{ApprovedPath: secretSock, ResolvedPath: secretResolved},
		},
	}
	if code := unixConnectExit(t, self, secretGrant, secretSock); code != 0 {
		t.Fatalf("exact socket under read-denied parent must connect: exit=%d", code)
	}
	if code, out := confinedRun(t, self, secretGrant, "/bin/cat", siblingFile); code == 0 {
		t.Fatalf("sibling file read under denied parent must fail: out=%s", out)
	}
}

// Remote IP access remains separate from local socket grants.
func TestSeatbeltDirectIPSeparationMatrix(t *testing.T) {
	self := requireSeatbelt(t)
	dir := shortTempDir(t)
	aPath := filepath.Join(dir, "a.sock")
	bPath := filepath.Join(dir, "b.sock")
	listenUnix(t, aPath)
	listenUnix(t, bPath)
	proj := filepath.Join(dir, "proj")
	testutil.FailErr(t, "mkdir", os.Mkdir(proj, 0o755))

	// Prefer a reachable non-loopback probe.
	remoteURL, cleanup := nonLoopbackProbe(t)
	if cleanup != nil {
		t.Cleanup(cleanup)
	}

	deny := confine.Confinement{Roots: []string{proj}, Network: confine.NetworkDeny}
	if remoteURL != "" {
		if code := confinedExit(t, self, deny, "/usr/bin/curl", "-s", "--max-time", "2", remoteURL); code == 0 {
			t.Fatal("default deny must block remote IP")
		}
	}
	if code := unixConnectExit(t, self, deny, aPath); code == 0 {
		t.Fatal("default deny must block AF_UNIX A")
	}

	direct := confine.Confinement{Roots: []string{proj}, Network: confine.NetworkDirectIP}
	profile, err := confine.BuildProfile(direct)
	testutil.FailErr(t, "BuildProfile direct", err)
	assertDirectProfileHonest(t, profile)

	if remoteURL != "" {
		if code := confinedExit(t, self, direct, "/usr/bin/curl", "-s", "--max-time", "2", remoteURL); code != 0 {
			// Transport failures are distinct from boundary denials.
			if code, out := confinedRun(t, self, direct, "/usr/bin/curl", "-s", "--max-time", "2", remoteURL); strings.Contains(strings.ToLower(out), "operation not permitted") || strings.Contains(out, "EPERM") {
				t.Fatalf("direct IP must not be Seatbelt-denied: exit=%d out=%s", code, out)
			}
		}
	} else if code, out := confinedRun(t, self, direct, "/usr/bin/python3", "-c",
		"import socket; s=socket.socket(socket.AF_INET, socket.SOCK_DGRAM); s.settimeout(1); s.connect(('192.0.2.1', 9)); print('ok')"); code != 0 &&
		(strings.Contains(strings.ToLower(out), "operation not permitted") || strings.Contains(out, "EPERM")) {
		t.Fatalf("direct IP UDP TEST-NET must not be Seatbelt-denied: exit=%d out=%s", code, out)
	}

	if code := unixConnectExit(t, self, direct, aPath); code == 0 {
		t.Fatal("direct IP must keep AF_UNIX A denied")
	}
	if code := unixConnectExit(t, self, direct, bPath); code == 0 {
		t.Fatal("direct IP must keep AF_UNIX B denied")
	}

	resolvedAPath, err := filepath.EvalSymlinks(aPath)
	testutil.FailErr(t, "eval a", err)
	directPlusA := confine.Confinement{
		Roots:   []string{proj},
		Network: confine.NetworkDirectIP,
		SocketGrants: []confine.SocketGrant{
			{ApprovedPath: aPath, ResolvedPath: resolvedAPath},
		},
	}
	if code := unixConnectExit(t, self, directPlusA, aPath); code != 0 {
		t.Fatalf("direct IP + socket A must connect A: exit=%d", code)
	}
	if code := unixConnectExit(t, self, directPlusA, bPath); code == 0 {
		t.Fatal("direct IP + socket A must keep B denied")
	}
}

func TestSeatbeltFailClosedStartupMatrix(t *testing.T) {
	requireSeatbelt(t)
	confine.TestingSetAutoConfine(t)
	t.Setenv("LYCAON_BYPASS_APPROVALS", "")
	t.Setenv("LYCAON_SANDBOX", "")
	t.Setenv("LYCAON_SANDBOX_NETWORK", "")
	t.Cleanup(confine.SetBrokerUnavailableForTest(fmt.Errorf("injected broker failure")))

	proj := t.TempDir()
	c, ok := confine.DefaultConfinement(confine.Request{Roots: []string{proj}})
	if !ok || c == nil || c.Network != confine.NetworkProxyOnly || c.ProxyAddr != "" {
		t.Fatalf("fail-closed confinement: ok=%v c=%+v", ok, c)
	}
	if _, err := confine.BindAction(c, confine.EgressCommand{ToolCallID: "failure-matrix"}); err == nil {
		t.Fatal("broker failure must refuse the action lease")
	}
	deg, yes := confine.EgressDegradedState()
	if !yes || deg.Reason == "" {
		t.Fatal("degraded reason required")
	}
}

func unixConnectExit(t *testing.T, self string, c confine.Confinement, sockPath string) int {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 required for AF_UNIX connect probe")
	}
	code, _ := confinedRun(t, self, c, "python3", "-c",
		"import socket,sys; s=socket.socket(socket.AF_UNIX); s.settimeout(1); s.connect(sys.argv[1])",
		sockPath)
	return code
}

func assertDirectProfileHonest(t *testing.T, profile string) {
	t.Helper()
	if strings.Contains(profile, "(allow network*)") {
		t.Fatalf("direct profile must not contain network*:\n%s", profile)
	}
	if strings.Contains(profile, "(allow network-outbound)\n") || strings.Contains(profile, "(allow network-outbound )") {
		t.Fatalf("direct profile must not bare-allow network-outbound:\n%s", profile)
	}
	if strings.Contains(profile, "(allow network-outbound (subpath") {
		t.Fatalf("direct profile must not use socket subpath:\n%s", profile)
	}
	if !strings.Contains(profile, `(allow network-outbound (remote ip "*:*"))`) {
		t.Fatalf("direct profile missing remote IP rule:\n%s", profile)
	}
}

func nonLoopbackProbe(t *testing.T) (url string, cleanup func()) {
	t.Helper()
	// Prefer a reachable host address.
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "", nil
	}
	var ip net.IP
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok || ipnet.IP.IsLoopback() || ipnet.IP.To4() == nil {
			continue
		}
		ip = ipnet.IP.To4()
		break
	}
	if ip == nil {
		return "", nil
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(ip.String(), "0"))
	if err != nil {
		return "", nil
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_, _ = conn.Write([]byte("ok"))
			_ = conn.Close()
		}
	}()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	if _, err := strconv.Atoi(port); err != nil {
		_ = ln.Close()
		return "", nil
	}
	return "http://" + net.JoinHostPort(ip.String(), port) + "/", func() { _ = ln.Close() }
}

// System resolution is available only with direct IP access.
func TestSeatbeltSystemResolverIsDirectIPOnly(t *testing.T) {
	self := requireSeatbelt(t)
	dir := shortTempDir(t)
	proj := filepath.Join(dir, "proj")
	testutil.FailErr(t, "mkdir", os.Mkdir(proj, 0o755))

	// The host's own mDNS name is answered by mDNSResponder without traffic.
	// Hosts-file names resolve in-process even under deny, so they prove nothing.
	name := mdnsSelfName(t)
	resolve := []string{"/usr/bin/python3", "-c", "import socket,sys; socket.gethostbyname(sys.argv[1])", name}

	direct := confine.Confinement{Roots: []string{proj}, Network: confine.NetworkDirectIP}
	if code, out := confinedRun(t, self, direct, resolve[0], resolve[1:]...); code != 0 {
		t.Fatalf("direct IP must resolve %s, else it is a literal-IP-only capability: exit=%d out=%s", name, code, out)
	}

	proxyEndpoint, err := net.Listen("tcp", "127.0.0.1:0")
	testutil.FailErr(t, "bind proxy endpoint", err)
	t.Cleanup(func() { testutil.FailErr(t, "close proxy endpoint", proxyEndpoint.Close()) })

	// Mediated and denied modes resolve through the action proxy.
	for _, mode := range []confine.NetworkMode{confine.NetworkProxyOnly, confine.NetworkDeny} {
		c := confine.Confinement{Roots: []string{proj}, Network: mode}
		if mode == confine.NetworkProxyOnly {
			c.ProxyAddr = proxyEndpoint.Addr().String()
		}
		if code, out := confinedRun(t, self, c, resolve[0], resolve[1:]...); code == 0 {
			t.Fatalf("%s must not resolve %s through the system resolver: out=%s",
				confine.NetworkLabel(mode), name, out)
		}
	}
}

// Declared transports narrow direct IP by protocol and port.
func TestSeatbeltDirectIPNarrowing(t *testing.T) {
	self := requireSeatbelt(t)
	dir := shortTempDir(t)
	proj := filepath.Join(dir, "proj")
	testutil.FailErr(t, "mkdir", os.Mkdir(proj, 0o755))

	narrowed := confine.Confinement{
		Roots:           []string{proj},
		Network:         confine.NetworkDirectIP,
		DirectIPPermits: []confine.DirectIPPermit{{Protocol: "udp", Port: 123}},
	}

	if got, want := directIPVerdicts(t, self, narrowed, "udp/123", "udp/53", "tcp/123", "tcp/443"),
		"udp/123 allowed\nudp/53 denied\ntcp/123 denied\ntcp/443 denied\n"; got != want {
		t.Fatalf("a udp/123 permit must admit only udp/123:\ngot:\n%swant:\n%s", got, want)
	}

	// Empty permits leave direct IP unrestricted.
	wide := confine.Confinement{Roots: []string{proj}, Network: confine.NetworkDirectIP}
	if got, want := directIPVerdicts(t, self, wide, "udp/53", "tcp/443"),
		"udp/53 allowed\ntcp/443 allowed\n"; got != want {
		t.Fatalf("unnarrowed direct IP must admit every transport:\ngot:\n%swant:\n%s", got, want)
	}

	// Direct IP rules cannot narrow the peer host.
	profile, err := confine.BuildProfile(narrowed)
	testutil.FailErr(t, "BuildProfile narrowed", err)
	if !strings.Contains(profile, `(remote udp "*:123")`) {
		t.Fatalf("narrowed profile must emit the declared transport: %s", profile)
	}
	if strings.Contains(profile, `(remote ip "*:*")`) {
		t.Fatalf("narrowed profile must not keep the wide-open allowance: %s", profile)
	}
}

// directIPVerdicts reports the sandbox verdict for each "proto/port" toward
// TEST-NET-1. Seatbelt's localhost covers every local interface address, so
// only a non-local peer reaches the direct-IP rules; TEST-NET-1 never answers,
// so the verdict is the connect or send errno, not a reply.
func directIPVerdicts(t *testing.T, self string, c confine.Confinement, transports ...string) string {
	t.Helper()
	probe := []string{"/usr/bin/python3", "-c", `import socket,sys
for spec in sys.argv[1:]:
    proto, port = spec.split("/")
    s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM if proto == "udp" else socket.SOCK_STREAM)
    s.setblocking(False)
    try:
        s.connect(("192.0.2.1", int(port)))
        if proto == "udp":
            s.send(b"x")
        verdict = "allowed"
    except PermissionError:
        verdict = "denied"
    except OSError:
        verdict = "allowed"
    print(spec, verdict)`}
	code, out, stderr := confinedStdout(t, self, c, probe[0], append(probe[1:], transports...)...)
	if code != 0 {
		t.Fatalf("verdict probe failed: exit=%d out=%s stderr=%s", code, out, stderr)
	}
	return out
}

func mdnsSelfName(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("/usr/sbin/scutil", "--get", "LocalHostName").Output()
	testutil.FailErr(t, "read LocalHostName", err)
	return strings.TrimSpace(string(out)) + ".local"
}

// TestDirectIPPermitsNarrowOnly verifies declarations cannot add access.
func TestDirectIPPermitsNarrowOnly(t *testing.T) {
	for _, tc := range []struct {
		name     string
		declared []string
		want     []confine.DirectIPPermit
	}{
		{"scheme narrows to one protocol", []string{"udp://time.nist.gov:123"},
			[]confine.DirectIPPermit{{Protocol: "udp", Port: 123}}},
		{"no scheme permits both", []string{"db.internal:5432"},
			[]confine.DirectIPPermit{{Protocol: "tcp", Port: 5432}, {Protocol: "udp", Port: 5432}}},
	} {
		got, narrowed := confine.ParseDirectIPPermits(tc.declared)
		if !narrowed || !slices.Equal(got, tc.want) {
			t.Fatalf("%s: got %v narrowed=%t want %v", tc.name, got, narrowed, tc.want)
		}
	}
	// Partial declarations do not claim an enforceable narrowing.
	for _, declared := range [][]string{
		nil,
		{""},
		{"no-port-here"},
		{"https://api.example.com"},
		{"udp://a:123", "not-a-dest"},
		{"udp://a:0"},
		{"udp://a:99999"},
	} {
		if got, narrowed := confine.ParseDirectIPPermits(declared); narrowed || got != nil {
			t.Fatalf("%v must not narrow, got %v", declared, got)
		}
	}
}

// Direct-IP declarations flow through profile construction and execution.
func TestDirectIPNarrowingEndToEnd(t *testing.T) {
	self := requireSeatbelt(t)
	confine.TestingSetAutoConfine(t)
	t.Setenv("LYCAON_BYPASS_APPROVALS", "")
	t.Setenv("LYCAON_SANDBOX", "")
	t.Setenv("LYCAON_SANDBOX_NETWORK", "")

	proj := t.TempDir()
	c, ok := confine.DefaultConfinement(confine.Request{
		Roots:            []string{proj},
		Egress:           confine.EgressDirectIP,
		DirectIPDeclared: []string{"udp://time.nist.gov:123"},
	})
	if !ok || c == nil {
		t.Fatalf("direct IP confinement must apply: ok=%v c=%+v", ok, c)
	}
	if c.Network != confine.NetworkDirectIP {
		t.Fatalf("network mode: got %s", confine.NetworkLabel(c.Network))
	}
	want := []confine.DirectIPPermit{{Protocol: "udp", Port: 123}}
	if !slices.Equal(c.DirectIPPermits, want) {
		t.Fatalf("declared strings must reach the confinement: got %v want %v", c.DirectIPPermits, want)
	}

	// The narrowed profile retains name resolution. Public NTP servers drop
	// requests under load, so any one of several may answer; a denial fails at once.
	ntp := []string{"/usr/bin/python3", "-c", `import socket,struct,sys
errors = []
for host in ("time.apple.com", "time.google.com", "pool.ntp.org", "time.nist.gov"):
    try:
        s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        s.settimeout(4)
        s.connect((socket.gethostbyname(host), 123))
        s.send(b"\x1b" + 47 * b"\0")
        print(struct.unpack("!12I", s.recv(48))[10] - 2208988800)
        sys.exit(0)
    except PermissionError:
        raise
    except OSError as error:
        errors.append(f"{host}: {error}")
sys.exit("no NTP server answered: " + "; ".join(errors))`}
	code, out, stderr := confinedStdout(t, self, *c, ntp[0], ntp[1:]...)
	if code != 0 {
		t.Fatalf("narrowed direct IP must complete an NTP round trip by name: exit=%d out=%s stderr=%s", code, out, stderr)
	}
	epoch, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	if err != nil {
		t.Fatalf("expected a unix timestamp from the server, got %q (%v; stderr=%s)", out, err, stderr)
	}
	// The response must contain a plausible timestamp.
	if epoch < 1_700_000_000 || epoch > 4_000_000_000 {
		t.Fatalf("implausible NTP timestamp %d — the exchange did not reach a real server", epoch)
	}

	// Undeclared transports remain denied.
	if code, out := confinedRun(t, self, *c, "/usr/bin/python3", "-c",
		"import socket;s=socket.socket();s.settimeout(3);s.connect(('1.1.1.1',443));print('tcp ok')"); code == 0 {
		t.Fatalf("undeclared tcp must stay denied on the end-to-end path: out=%s", out)
	}
}
